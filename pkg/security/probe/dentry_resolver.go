// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

// +build linux

package probe

import (
	"C"
	"fmt"
	"unsafe"

	lib "github.com/DataDog/ebpf"
	"github.com/DataDog/ebpf/manager"
	lru "github.com/hashicorp/golang-lru"
	"github.com/pkg/errors"

	"github.com/DataDog/datadog-agent/pkg/security/ebpf"
	"github.com/DataDog/datadog-agent/pkg/security/model"
	"github.com/DataDog/datadog-go/statsd"
)

const (
	dentryPathKeyNotFound = "error: dentry path key not found"
	fakeInodeMSW          = 0xdeadc001
)

// DentryResolver resolves inode/mountID to full paths
type DentryResolver struct {
	client    *statsd.Client
	pathnames *lib.Map
	cache     map[uint32]*lru.Cache
}

// ErrInvalidKeyPath is returned when inode or mountid are not valid
type ErrInvalidKeyPath struct {
	Inode   uint64
	MountID uint32
}

func (e *ErrInvalidKeyPath) Error() string {
	return fmt.Sprintf("invalid inode/mountID couple: %d/%d", e.Inode, e.MountID)
}

// ErrEntryNotFound is thrown when a path key was not found in the cache
var ErrEntryNotFound = errors.New("entry not found")

// PathKey identifies an entry in the dentry cache
type PathKey struct {
	Inode   uint64
	MountID uint32
	PathID  uint32
}

func (p *PathKey) Write(buffer []byte) {
	ebpf.ByteOrder.PutUint64(buffer[0:8], p.Inode)
	ebpf.ByteOrder.PutUint32(buffer[8:12], p.MountID)
	ebpf.ByteOrder.PutUint32(buffer[12:16], p.PathID)
}

// IsNull returns true if a key is invalid
func (p *PathKey) IsNull() bool {
	return p.Inode == 0 && p.MountID == 0
}

func (p *PathKey) String() string {
	return fmt.Sprintf("%x/%x", p.MountID, p.Inode)
}

// MarshalBinary returns the binary representation of a path key
func (p *PathKey) MarshalBinary() ([]byte, error) {
	if p.IsNull() {
		return nil, &ErrInvalidKeyPath{Inode: p.Inode, MountID: p.MountID}
	}

	return make([]byte, 16), nil
}

// PathValue describes a value of an entry of the cache
type PathValue struct {
	Parent PathKey
	Name   [model.MaxSegmentLength + 1]byte
}

// GetName returns the path value as a string
func (pv *PathValue) GetName() string {
	return C.GoString((*C.char)(unsafe.Pointer(&pv.Name)))
}

// DelCacheEntry removes an entry from the cache
func (dr *DentryResolver) DelCacheEntry(mountID uint32, inode uint64) {
	if entries, exists := dr.cache[mountID]; exists {
		key := PathKey{Inode: inode}

		// Delete path recursively
		for {
			path, exists := entries.Get(key.Inode)
			if !exists {
				break
			}
			entries.Remove(key.Inode)

			parent := path.(PathValue).Parent
			if parent.Inode == 0 {
				break
			}

			// Prepare next key
			key = parent
		}
	}
}

// DelCacheEntries removes all the entries belonging to a mountID
func (dr *DentryResolver) DelCacheEntries(mountID uint32) {
	delete(dr.cache, mountID)
}

func (dr *DentryResolver) lookupInodeFromCache(mountID uint32, inode uint64) (pathValue PathValue, err error) {
	entries, exists := dr.cache[mountID]
	if !exists {
		return pathValue, ErrEntryNotFound
	}

	entry, exists := entries.Get(inode)
	if !exists {
		return pathValue, ErrEntryNotFound
	}

	return entry.(PathValue), nil
}

func (dr *DentryResolver) cacheInode(mountID uint32, inode uint64, pathValue PathValue) error {
	entries, exists := dr.cache[mountID]
	if !exists {
		var err error

		entries, err = lru.New(128)
		if err != nil {
			return err
		}
		dr.cache[mountID] = entries
	}

	entries.Add(inode, pathValue)

	return nil
}

func (dr *DentryResolver) getNameFromCache(mountID uint32, inode uint64) (pathValue PathValue, err error) {
	if pathValue, err = dr.lookupInodeFromCache(mountID, inode); err != nil {
		_ = dr.client.Count(MetricDentryResolverMiss, 1, cacheTag, 1.0)
	} else {
		_ = dr.client.Count(MetricDentryResolverHits, 1, cacheTag, 1.0)
	}

	return
}

func (dr *DentryResolver) getNameFromMap(mountID uint32, inode uint64, pathID uint32) (path PathValue, err error) {
	key := PathKey{MountID: mountID, Inode: inode, PathID: pathID}

	if err := dr.pathnames.Lookup(key, &path); err != nil {
		_ = dr.client.Count(MetricDentryResolverMiss, 1, kernelMapsTag, 1.0)
		return path, fmt.Errorf("unable to get filename for mountID `%d` and inode `%d`", mountID, inode)
	}

	_ = dr.client.Count(MetricDentryResolverHits, 1, kernelMapsTag, 1.0)
	return path, nil
}

// GetName resolves a couple of mountID/inode to a path
func (dr *DentryResolver) GetName(mountID uint32, inode uint64, pathID uint32) string {
	pathValue, err := dr.getNameFromCache(mountID, inode)
	if err != nil {
		pathValue, err = dr.getNameFromMap(mountID, inode, pathID)
	}

	if err != nil {
		return ""
	}
	return pathValue.GetName()
}

// resolveFromCache resolves path from the cache
func (dr *DentryResolver) resolveFromCache(mountID uint32, inode uint64) (filename string, err error) {
	var path PathValue
	depth := int64(0)
	key := PathKey{MountID: mountID, Inode: inode}

	// Fetch path recursively
	for {
		path, err = dr.lookupInodeFromCache(key.MountID, key.Inode)
		if err != nil {
			_ = dr.client.Count(MetricDentryResolverMiss, 1, cacheTag, 1.0)
			break
		}
		depth++

		// Don't append dentry name if this is the root dentry (i.d. name == '/')
		if path.Name[0] != '\x00' && path.Name[0] != '/' {
			filename = "/" + path.GetName() + filename
		}

		if path.Parent.Inode == 0 {
			if len(filename) == 0 {
				filename = "/"
			}
			break
		}

		// Prepare next key
		key = path.Parent
	}

	if depth > 0 {
		_ = dr.client.Count(MetricDentryResolverHits, depth, cacheTag, 1.0)
	}

	return
}

// resolveFromMap resolves from kernel map
func (dr *DentryResolver) resolveFromMap(mountID uint32, inode uint64, pathID uint32) (filename string, _ error) {
	key := PathKey{MountID: mountID, Inode: inode, PathID: pathID}
	var path PathValue
	var segment string
	var err, resolutionErr error
	var truncatedParentsErr ErrTruncatedParents
	var truncatedSegmentErr ErrTruncatedSegment

	keyBuffer, err := key.MarshalBinary()
	if err != nil {
		return "", err
	}

	depth := int64(0)
	toAdd := make(map[PathKey]PathValue)

	// Fetch path recursively
	for {
		key.Write(keyBuffer)
		if err = dr.pathnames.Lookup(keyBuffer, &path); err != nil {
			filename = dentryPathKeyNotFound
			_ = dr.client.Count(MetricDentryResolverMiss, 1, kernelMapsTag, 1.0)
			break
		}
		depth++

		cacheKey := PathKey{MountID: key.MountID, Inode: key.Inode}
		toAdd[cacheKey] = path

		if path.Name[0] == '\x00' {
			resolutionErr = truncatedParentsErr
			break
		}

		// Don't append dentry name if this is the root dentry (i.d. name == '/')
		if path.Name[0] != '/' {
			segment = C.GoString((*C.char)(unsafe.Pointer(&path.Name)))
			if len(segment) >= (model.MaxSegmentLength) {
				resolutionErr = truncatedSegmentErr
			}
			filename = "/" + segment + filename
		}

		if path.Parent.Inode == 0 {
			break
		}

		// Prepare next key
		key = path.Parent
	}

	if depth > 0 {
		_ = dr.client.Count(MetricDentryResolverHits, depth, kernelMapsTag, 1.0)
	}

	// resolution errors are more important than regular map lookup errors
	if resolutionErr != nil {
		err = resolutionErr
	}

	if len(filename) == 0 {
		filename = "/"
	}

	if err == nil {
		for k, v := range toAdd {
			// do not cache fake path keys in the case of rename events
			if k.Inode>>32 != fakeInodeMSW {
				_ = dr.cacheInode(k.MountID, k.Inode, v)
			}
		}
	}

	return filename, err
}

// Resolve the pathname of a dentry, starting at the pathnameKey in the pathnames table
func (dr *DentryResolver) Resolve(mountID uint32, inode uint64, pathID uint32) (string, error) {
	path, err := dr.resolveFromCache(mountID, inode)
	if err != nil {
		path, err = dr.resolveFromMap(mountID, inode, pathID)
	}
	return path, err
}

func (dr *DentryResolver) resolveParentFromCache(mountID uint32, inode uint64) (uint32, uint64, error) {
	path, err := dr.getNameFromCache(mountID, inode)
	if err != nil {
		return 0, 0, ErrEntryNotFound
	}

	return path.Parent.MountID, path.Parent.Inode, nil
}

func (dr *DentryResolver) resolveParentFromMap(mountID uint32, inode uint64, pathID uint32) (uint32, uint64, error) {
	path, err := dr.getNameFromMap(mountID, inode, pathID)
	if err != nil {
		return 0, 0, err
	}

	return path.Parent.MountID, path.Parent.Inode, nil
}

// GetParent - Return the parent mount_id/inode
func (dr *DentryResolver) GetParent(mountID uint32, inode uint64, pathID uint32) (uint32, uint64, error) {
	parentMountID, parentInode, err := dr.resolveParentFromCache(mountID, inode)
	if err != nil {
		parentMountID, parentInode, err = dr.resolveParentFromMap(mountID, inode, pathID)
	}
	return parentMountID, parentInode, err
}

// Start the dentry resolver
func (dr *DentryResolver) Start(manager *manager.Manager) error {
	pathnames, ok, err := manager.GetMap("pathnames")
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("map pathnames not found")
	}
	dr.pathnames = pathnames

	return nil
}

// ErrTruncatedSegment is used to notify that a segment of the path was truncated because it was too long
type ErrTruncatedSegment struct{}

func (err ErrTruncatedSegment) Error() string {
	return "truncated_segment"
}

// ErrTruncatedParents is used to notify that some parents of the path are missing
type ErrTruncatedParents struct{}

func (err ErrTruncatedParents) Error() string {
	return "truncated_parents"
}

// NewDentryResolver returns a new dentry resolver
func NewDentryResolver(client *statsd.Client) (*DentryResolver, error) {
	return &DentryResolver{
		client: client,
		cache:  make(map[uint32]*lru.Cache),
	}, nil
}
