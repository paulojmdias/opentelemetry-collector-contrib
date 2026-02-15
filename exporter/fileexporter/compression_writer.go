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
// It closes and resets the encoder after each Write() call so that every
// write produces a complete, independently decompressible zstd frame.
// This is essential for file rotation (via timberjack): since timberjack
// can silently switch to a new file between writes, each file segment must
// contain only complete frames. The zstd decoder handles concatenated
// frames natively.
//
// Note: zstd.Encoder.Flush() only performs a block-level flush within an
// open frame, it does NOT write the "last block" marker or CRC that make
// the frame independently decompressible. Only Close() finalizes a frame.
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
		return zstd.NewWriter(c.base,
			zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(c.level)),
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

	// Close the encoder to finalize the current zstd frame with the
	// "last block" marker and CRC checksum. This makes the frame
	// independently decompressible, which is required so that when
	// timberjack rotates the underlying file, each file contains only
	// complete frames.
	if err := c.closeAndResetEncoder(); err != nil {
		c.err = err
		return n, err
	}

	return n, nil
}

// closeAndResetEncoder finalizes the current zstd frame by calling Close()
// on the encoder, then resets it for the next write. Close() writes the
// "last block" marker and CRC, producing a complete frame. Reset() reuses
// the encoder's allocated buffers for efficiency.
func (c *compressingWriter) closeAndResetEncoder() error {
	if err := c.encoder.Close(); err != nil {
		return err
	}

	// Reset the encoder so the next Write() starts a new frame.
	if enc, ok := c.encoder.(*zstd.Encoder); ok {
		enc.Reset(c.base)
	}
	c.dirty = false
	return nil
}

// Close finalizes the compression stream and closes the underlying writer.
func (c *compressingWriter) Close() error {
	// Close the encoder to finalize any in-progress frame and release resources.
	// After closeAndResetEncoder in Write(), dirty is false and the encoder
	// has been reset, but it still needs to be closed to release resources.
	encoderErr := c.encoder.Close()
	baseErr := c.base.Close()
	return errors.Join(encoderErr, baseErr)
}

// flush is called by the flusher goroutine in fileWriter.
// It finalizes the current frame if dirty, ensuring data is fully written
// to the underlying writer as complete zstd frames.
func (c *compressingWriter) flush() error {
	if !c.dirty {
		return nil
	}
	return c.closeAndResetEncoder()
}
