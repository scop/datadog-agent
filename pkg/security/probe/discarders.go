// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-2020 Datadog, Inc.

// +build linux

package probe

import (
	"time"

	"github.com/DataDog/datadog-agent/pkg/security/ebpf"
	"github.com/DataDog/datadog-agent/pkg/security/rules"
	"github.com/DataDog/datadog-agent/pkg/security/secl/eval"
)

const (
	DiscardInodeOp = 1
	DiscardPidOp
	SpanIDOp
	GoroutineTrackerOp
)

func discardMarshalHeader(req *ERPCRequest, eventType EventType, timeout uint64) int {
	ebpf.ByteOrder.PutUint64(req.Data[0:8], uint64(eventType))
	ebpf.ByteOrder.PutUint64(req.Data[8:16], uint64(timeout))

	return 16
}

func (p *Probe) discardPID(eventType EventType, pid uint32) error {
	req := ERPCRequest{
		OP: DiscardPidOp,
	}

	offset := discardMarshalHeader(&req, eventType, 0)
	ebpf.ByteOrder.PutUint32(req.Data[offset:offset+4], pid)

	return p.erpc.Request(&req)
}

func (p *Probe) discardPIDWithTimeout(eventType EventType, pid uint32, timeout time.Duration) error {
	req := ERPCRequest{
		OP: DiscardPidOp,
	}

	offset := discardMarshalHeader(&req, eventType, uint64(timeout.Nanoseconds()))
	ebpf.ByteOrder.PutUint32(req.Data[offset:offset+4], pid)

	return p.erpc.Request(&req)
}

type inodeDiscarder struct {
	PathKey  PathKey
	Revision uint32
	Padding  uint32
}

type inodeDiscarderParameters struct {
	EventType EventType
}

func (p *Probe) removeDiscarderInode(mountID uint32, inode uint64) {
	key := inodeDiscarder{
		PathKey: PathKey{
			MountID: mountID,
			Inode:   inode,
		},
	}
	_ = p.inodeDiscarders.Delete(&key)
}

func (p *Probe) discardInode(eventType EventType, mountID uint32, inode uint64) error {
	req := ERPCRequest{
		OP: DiscardInodeOp,
	}

	offset := discardMarshalHeader(&req, eventType, 0)
	ebpf.ByteOrder.PutUint64(req.Data[offset:offset+8], inode)
	ebpf.ByteOrder.PutUint32(req.Data[offset+8:offset+12], mountID)

	return p.erpc.Request(&req)
}

func (p *Probe) discardParentInode(rs *rules.RuleSet, eventType EventType, field eval.Field, filename string, mountID uint32, inode uint64, pathID uint32) (bool, uint32, uint64, error) {
	isDiscarder, err := isParentPathDiscarder(rs, p.regexCache, eventType, field, filename)
	if !isDiscarder {
		return false, 0, 0, err
	}

	parentMountID, parentInode, err := p.resolvers.DentryResolver.GetParent(mountID, inode, pathID)
	if err != nil {
		return false, 0, 0, err
	}

	if err := p.discardInode(eventType, parentMountID, parentInode); err != nil {
		return false, 0, 0, err
	}

	return true, parentMountID, parentInode, nil
}
