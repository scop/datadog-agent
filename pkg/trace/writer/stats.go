// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package writer

import (
	"compress/gzip"
	"io"
	"math"
	"strings"
	"sync/atomic"
	"time"

	"github.com/DataDog/datadog-agent/pkg/trace/config"
	"github.com/DataDog/datadog-agent/pkg/trace/info"
	"github.com/DataDog/datadog-agent/pkg/trace/logutil"
	"github.com/DataDog/datadog-agent/pkg/trace/metrics"
	"github.com/DataDog/datadog-agent/pkg/trace/metrics/timing"
	"github.com/DataDog/datadog-agent/pkg/trace/pb"
	"github.com/DataDog/datadog-agent/pkg/util/log"

	"github.com/tinylib/msgp/msgp"
)

// pathStats is the target host API path for delivering stats.
const pathStats = "/api/v0.2/stats"

const (
	// bytesPerEntry specifies the approximate size an entry in a stat payload occupies.
	bytesPerEntry = 375
	// maxEntriesPerPayload is the maximum number of entries in a stat payload. An
	// entry has an average size of 375 bytes in a compressed payload. The current
	// Datadog intake API limits a compressed payload to ~3MB (8,000 entries), but
	// let's have the default ensure we don't have paylods > 1.5 MB (4,000
	// entries).
	maxEntriesPerPayload = 4000
)

// StatsWriter ingests stats buckets and flushes them to the API.
type StatsWriter struct {
	in      <-chan []*pb.ClientStatsPayload
	senders []*sender
	stop    chan struct{}
	stats   *info.StatsWriterInfo

	easylog *logutil.ThrottledLogger
}

// NewStatsWriter returns a new StatsWriter. It must be started using Run.
func NewStatsWriter(cfg *config.AgentConfig, in <-chan []*pb.ClientStatsPayload) *StatsWriter {
	sw := &StatsWriter{
		in:      in,
		stats:   &info.StatsWriterInfo{},
		stop:    make(chan struct{}),
		easylog: logutil.NewThrottled(5, 10*time.Second), // no more than 5 messages every 10 seconds
	}
	climit := cfg.StatsWriter.ConnectionLimit
	if climit == 0 {
		// Allow 1% of the connection limit to outgoing sends. The original
		// connection limit was removed and used to be 2000 (1% = 20)
		climit = 20
	}
	qsize := cfg.StatsWriter.QueueSize
	if qsize == 0 {
		payloadSize := float64(maxEntriesPerPayload * bytesPerEntry)
		// default to 25% of maximum memory.
		maxmem := cfg.MaxMemory / 4
		if maxmem == 0 {
			// or 250MB if unbound
			maxmem = 250 * 1024 * 1024
		}
		qsize = int(math.Max(1, maxmem/payloadSize))
	}
	log.Debugf("Stats writer initialized (climit=%d qsize=%d)", climit, qsize)
	sw.senders = newSenders(cfg, sw, pathStats, climit, qsize)
	return sw
}

// Run starts the StatsWriter, making it ready to receive stats and report metrics.
func (w *StatsWriter) Run() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	defer close(w.stop)
	for {
		select {
		case stats := <-w.in:
			w.addStats(stats)
		case <-t.C:
			w.report()
		case <-w.stop:
			return
		}
	}
}

// Stop stops a running StatsWriter.
func (w *StatsWriter) Stop() {
	w.stop <- struct{}{}
	<-w.stop
	stopSenders(w.senders)
}

func (w *StatsWriter) addStats(sp []*pb.ClientStatsPayload) {
	defer timing.Since("datadog.trace_agent.stats_writer.encode_ms", time.Now())
	for _, p := range sp {
		payloads, bucketCount, entryCount := w.buildPayloads(p, maxEntriesPerPayload)
		switch n := len(payloads); {
		case n == 0:
			return
		case n > 1:
			atomic.AddInt64(&w.stats.Splits, 1)
		}
		atomic.AddInt64(&w.stats.StatsBuckets, int64(bucketCount))
		log.Debugf("Flushing %d entries (buckets=%d payloads=%v)", entryCount, bucketCount, len(payloads))
		for _, p := range payloads {
			w.SendPayload(p)
		}
	}
}

// SendPayload sends a stats payload to the Datadog backend.
func (w *StatsWriter) SendPayload(p *pb.ClientStatsPayload) {
	req := newPayload(map[string]string{
		headerLanguages:    strings.Join(info.Languages(), "|"),
		"Content-Type":     "application/msgpack",
		"Content-Encoding": "gzip",
	})
	if err := encodePayload(req.body, p); err != nil {
		log.Errorf("Stats encoding error: %v", err)
		return
	}
	atomic.AddInt64(&w.stats.Bytes, int64(req.body.Len()))
	sendPayloads(w.senders, req)
}

// encodePayload encodes the payload as Gzipped msgPack into w.
func encodePayload(w io.Writer, payload *pb.ClientStatsPayload) error {
	gz, err := gzip.NewWriterLevel(w, gzip.BestSpeed)
	if err != nil {
		return err
	}
	defer func() {
		if err := gz.Close(); err != nil {
			log.Errorf("Error closing gzip stream when writing stats payload: %v", err)
		}
	}()
	return msgp.Encode(gz, payload)
}

// buildPayloads returns a set of payload to send out, each payloads guaranteed
// to have the number of stats buckets under the given maximum.
func (w *StatsWriter) buildPayloads(p *pb.ClientStatsPayload, maxEntriesPerPayloads int) ([]*pb.ClientStatsPayload, int, int) {
	if len(p.Stats) == 0 {
		return nil, 0, 0
	}
	// 1. Get how many payloads we need, based on the total number of entries.
	nbEntries := 0
	for _, b := range p.Stats {
		nbEntries += len(b.Stats)
	}
	if maxEntriesPerPayloads <= 0 || nbEntries < maxEntriesPerPayloads {
		// nothing to do, break early
		return []*pb.ClientStatsPayload{p}, len(p.Stats), nbEntries
	}
	nbPayloads := nbEntries / maxEntriesPerPayloads
	if nbEntries%maxEntriesPerPayloads != 0 {
		nbPayloads++
	}

	// 2. Initialize a slice of nbPayloads indexes maps, mapping a time window (stat +
	//    duration) to a stats payload.
	type timeWindow struct{ start, duration uint64 }
	indexes := make([]map[timeWindow]int, nbPayloads)
	payloads := make([]*pb.ClientStatsPayload, nbPayloads)
	for i := 0; i < nbPayloads; i++ {
		indexes[i] = make(map[timeWindow]int, nbPayloads)
		payloads[i] = &pb.ClientStatsPayload{
			Hostname: p.Hostname,
			Env:      p.Env,
			Version:  p.Version,
			Stats:    make([]pb.ClientStatsBucket, 0, maxEntriesPerPayload),
		}
	}
	// 3. Iterate over all entries of each stats. Add the entry to one of
	//    the payloads, in a round robin fashion. Use the indexes maps to
	//    ensure that we have one ClientStatsBucket per timeWindow for each ClientStatsPayoad.
	i := 0
	nbStats := 0
	for _, b := range p.Stats {
		tw := timeWindow{b.Start, b.Duration}
		for _, g := range b.Stats {
			j := i % nbPayloads
			indexMap := indexes[j]
			bi, ok := indexMap[tw]
			if !ok {
				nbStats++
				bi = len(payloads[j].Stats)
				indexMap[tw] = bi
				payloads[j].Stats = append(payloads[j].Stats, pb.ClientStatsBucket{Start: tw.start, Duration: tw.duration})
			}
			// here, we can just append the group, because there is no duplicate groups in the original stats payloads sent to the writer.
			payloads[j].Stats[bi].Stats = append(payloads[j].Stats[bi].Stats, g)
			i++
		}
	}
	return payloads, nbStats, nbEntries
}

var _ eventRecorder = (*StatsWriter)(nil)

func (w *StatsWriter) report() {
	metrics.Count("datadog.trace_agent.stats_writer.payloads", atomic.SwapInt64(&w.stats.Payloads, 0), nil, 1)
	metrics.Count("datadog.trace_agent.stats_writer.stats_buckets", atomic.SwapInt64(&w.stats.StatsBuckets, 0), nil, 1)
	metrics.Count("datadog.trace_agent.stats_writer.bytes", atomic.SwapInt64(&w.stats.Bytes, 0), nil, 1)
	metrics.Count("datadog.trace_agent.stats_writer.retries", atomic.SwapInt64(&w.stats.Retries, 0), nil, 1)
	metrics.Count("datadog.trace_agent.stats_writer.splits", atomic.SwapInt64(&w.stats.Splits, 0), nil, 1)
	metrics.Count("datadog.trace_agent.stats_writer.errors", atomic.SwapInt64(&w.stats.Errors, 0), nil, 1)
}

// recordEvent implements eventRecorder.
func (w *StatsWriter) recordEvent(t eventType, data *eventData) {
	if data != nil {
		metrics.Histogram("datadog.trace_agent.stats_writer.connection_fill", data.connectionFill, nil, 1)
		metrics.Histogram("datadog.trace_agent.stats_writer.queue_fill", data.queueFill, nil, 1)
	}
	switch t {
	case eventTypeRetry:
		log.Debugf("Retrying to flush stats payload (error: %q)", data.err)
		atomic.AddInt64(&w.stats.Retries, 1)

	case eventTypeSent:
		log.Debugf("Flushed stats to the API; time: %s, bytes: %d", data.duration, data.bytes)
		timing.Since("datadog.trace_agent.stats_writer.flush_duration", time.Now().Add(-data.duration))
		atomic.AddInt64(&w.stats.Bytes, int64(data.bytes))
		atomic.AddInt64(&w.stats.Payloads, 1)

	case eventTypeRejected:
		log.Warnf("Stats writer payload rejected by edge: %v", data.err)
		atomic.AddInt64(&w.stats.Errors, 1)

	case eventTypeDropped:
		w.easylog.Warn("Stats writer queue full. Payload dropped (%.2fKB).", float64(data.bytes)/1024)
		metrics.Count("datadog.trace_agent.stats_writer.dropped", 1, nil, 1)
		metrics.Count("datadog.trace_agent.stats_writer.dropped_bytes", int64(data.bytes), nil, 1)
	}
}
