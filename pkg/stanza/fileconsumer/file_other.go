// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package fileconsumer // import "github.com/open-telemetry/opentelemetry-collector-contrib/pkg/stanza/fileconsumer"

import (
	"context"
	"sync"

	"go.uber.org/zap"

	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/stanza/fileconsumer/internal/reader"
)

// Take care of files which disappeared from the pattern since the last poll cycle
// this can mean either files which were removed, or rotated into a name not matching the pattern
// we do this before reading existing files to ensure we emit older log lines before newer ones
func (m *Manager) readLostFiles(ctx context.Context) {
	if m.readerFactory.DeleteAtEOF {
		// Lost files are not expected when delete_at_eof is enabled
		// since we are deleting the files before they can become lost.
		return
	}
	previousPollFiles := m.tracker.PreviousPollFiles()
	lostReaders := make([]*reader.Reader, 0, len(previousPollFiles))
OUTER:
	for _, oldReader := range previousPollFiles {
		for _, newReader := range m.tracker.CurrentPollFiles() {
			m.set.Logger.Debug("lost-check",
				zap.String("old_path", oldReader.GetFileName()),
				zap.String("new_path", newReader.GetFileName()),
				zap.Int("old_fp_len", oldReader.Fingerprint.Len()),
				zap.Int("new_fp_len", newReader.Fingerprint.Len()),
				zap.String("old_fp_head", oldReader.Fingerprint.HexHead(16)),
				zap.String("new_fp_head", newReader.Fingerprint.HexHead(16)),
			)
			if newReader.Fingerprint.StartsWith(oldReader.Fingerprint) {
				m.set.Logger.Debug("lost-check: startswith matched, treating as not-lost",
					zap.String("old_path", oldReader.GetFileName()),
					zap.String("new_path", newReader.GetFileName()),
				)
				continue OUTER
			}

			if !newReader.NameEquals(oldReader) {
				continue
			}

			// At this point, we know that the file has been rotated out of the matching pattern.
			// However, we do not know if it was moved or truncated.
			// If truncated, then both handles point to the same file, in which case
			// we should only read from it using the new reader. We can use
			// the Validate method to ensure that the file has not been truncated.
			if !oldReader.Validate() {
				m.set.Logger.Debug("File has been rotated(truncated)", zap.String("path", oldReader.GetFileName()))
				continue OUTER
			}
			// oldreader points to the rotated file after the move/rename. We can still read from it.
			m.set.Logger.Debug("File has been rotated(moved)", zap.String("path", oldReader.GetFileName()))
		}
		lostReaders = append(lostReaders, oldReader)
	}

	var lostWG sync.WaitGroup
	for _, lostReader := range lostReaders {
		lostWG.Add(1)
		m.set.Logger.Debug("Reading lost file", zap.String("path", lostReader.GetFileName()))
		go func(r *reader.Reader) {
			defer lostWG.Done()
			m.telemetryBuilder.FileconsumerReadingFiles.Add(ctx, 1)
			r.ReadToEnd(ctx)
			m.telemetryBuilder.FileconsumerReadingFiles.Add(ctx, -1)
		}(lostReader)
	}
	lostWG.Wait()
}
