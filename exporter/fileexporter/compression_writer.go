// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package fileexporter // import "github.com/open-telemetry/opentelemetry-collector-contrib/exporter/fileexporter"

import (
	"errors"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
)

// compressingWriter wraps an io.WriteCloser with streaming zstd compression.
// It flushes a complete frame after each Write() call so that file rotation
// (via timberjack) always splits at valid frame boundaries. The zstd decoder
// handles concatenated frames natively.
//
// Thread safety: this type is not independently thread-safe. All calls are
// serialized by the fileWriter.mutex in the caller. Do not use this type
// from multiple goroutines without external synchronization.
type compressingWriter struct {
	base        io.WriteCloser // underlying writer (file or timberjack)
	compression string
	level       int
	encoder     io.WriteCloser // zstd.Encoder
	dirty       bool           // tracks whether encoder has received data since last flush/creation
	err         error          // sticky error state
}

func newCompressingWriter(base io.WriteCloser, compression string, level int) (*compressingWriter, error) {
	cw := &compressingWriter{
		base:        base,
		compression: compression,
		level:       level,
	}

	encoder, err := cw.newEncoder()
	if err != nil {
		return nil, err
	}
	cw.encoder = encoder

	return cw, nil
}

func (c *compressingWriter) newEncoder() (io.WriteCloser, error) {
	switch c.compression {
	case compressionZSTD:
		encoderLevel := mapZstdCompressionLevel(c.level)
		return zstd.NewWriter(c.base,
			zstd.WithEncoderLevel(encoderLevel),
			zstd.WithEncoderConcurrency(1),
		)
	default:
		return nil, fmt.Errorf("unsupported compression: %s", c.compression)
	}
}

func (c *compressingWriter) Write(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}

	n, err := c.encoder.Write(p)
	if err != nil {
		c.err = err
		return n, err
	}
	c.dirty = true

	// Flush after each write to create complete frame boundaries.
	// This ensures that when timberjack rotates the underlying file,
	// each file segment contains only complete frames that are
	// independently decompressible.
	if err := c.flushFrame(); err != nil {
		c.err = err
		return n, err
	}

	return n, nil
}

func (c *compressingWriter) flushFrame() error {
	if !c.dirty {
		return nil
	}

	// zstd Flush() completes the current frame, making it independently decompressible.
	if f, ok := c.encoder.(interface{ Flush() error }); ok {
		if err := f.Flush(); err != nil {
			return err
		}
	}
	c.dirty = false
	return nil
}

// Close finalizes the compression stream and closes the underlying writer.
func (c *compressingWriter) Close() error {
	var encoderErr error
	if c.dirty {
		encoderErr = c.encoder.Close()
	} else if c.err == nil {
		// Encoder has no pending data, but still needs to be closed to release resources.
		encoderErr = c.encoder.Close()
	}
	baseErr := c.base.Close()
	return errors.Join(encoderErr, baseErr)
}

// flush is called by the flusher goroutine in fileWriter.
func (c *compressingWriter) flush() error {
	return c.flushFrame()
}

// mapZstdCompressionLevel maps a generic level (0-22) to zstd.EncoderLevel.
func mapZstdCompressionLevel(level int) zstd.EncoderLevel {
	switch {
	case level <= 0:
		return zstd.SpeedDefault
	case level <= 3:
		return zstd.SpeedFastest
	case level <= 7:
		return zstd.SpeedDefault
	case level <= 12:
		return zstd.SpeedBetterCompression
	default:
		return zstd.SpeedBestCompression
	}
}
