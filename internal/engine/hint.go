package engine

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
)

// Hint files: a compact key-to-position map written next to a data
// file once the file's contents stop changing (at rotation and at
// Close). A hint is an accelerator, never a correctness dependency:
// when a hint is missing or damaged, Open rebuilds the same index by
// scanning the data file itself.
//
// A hint file is named after its data file with the .hint extension
// and holds one record per entry of the data file, in entry order.
// Each record locates the entry without its value:
//
//	+--------+------------+------------+------------+-------------+
//	| crc 4B | keyLen 4B  | valueLen 4B| offset 8B  | key (bytes) |
//	+--------+------------+------------+------------+-------------+
//
// All integers are little-endian. offset is the entry's start offset
// inside the data file; the entry's encoded size is derivable, so it
// is not stored. valueLen carries the same tombstone sentinel as the
// entry header (tombstoneValueLen marks a delete record), which makes
// replaying a hint order-equivalent to replaying the data file. The
// CRC-32 (IEEE) covers every byte after the crc field.

// hintFileExt is the extension of hint files.
const hintFileExt = ".hint"

// hintTmpSuffix is the suffix of a hint file while it is being written;
// it is renamed into place once complete and synced. A leftover from a
// crash is never read — it is removed at Open and at merge.
const hintTmpSuffix = ".tmp"

// hintRecordHeaderSize is the fixed-size prefix of a hint record.
const hintRecordHeaderSize = 4 + 4 + 4 + 8

// Offsets of the fields inside a hint record header.
const (
	hintKeyLenOff   = 4
	hintValueLenOff = 8
	hintOffsetOff   = 12
)

// hintFileName returns the hint file name for a data file ID.
func hintFileName(id uint32) string {
	return fmt.Sprintf("%010d%s", id, hintFileExt)
}

// removeHintTmpResidue deletes every leftover ".hint.tmp" file in dir
// — the remains of a hint write interrupted by a crash. A residue file
// is never read: publication goes through rename, so the final
// ".hint" name is either absent or complete, and the tmp name carries
// no information worth keeping.
func removeHintTmpResidue(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("engine: list %q for hint residue: %w", dir, err)
	}
	removed := false
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), hintFileExt+hintTmpSuffix) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("engine: remove hint residue %q: %w", entry.Name(), err)
		}
		removed = true
	}
	if removed {
		return syncDir(dir)
	}
	return nil
}

// entrySizeFromHint returns the encoded size of the data entry a hint
// record with the given keyLen/valueLen points at.
func entrySizeFromHint(keyLen, valueLen uint32) int64 {
	total := int64(entryHeaderSize) + int64(keyLen)
	if valueLen != tombstoneValueLen {
		total += int64(valueLen)
	}
	return total
}

// hintRecordCRC computes the checksum of a hint record body: every
// byte after the crc field. header is the record's fixed-size prefix
// with the crc field already holding any value (it is skipped).
func hintRecordCRC(header, key []byte) uint32 {
	return crc32.Update(crc32.ChecksumIEEE(header[4:]), crc32.IEEETable, key)
}

// writeHintFile durably writes the hint file for the data file id,
// whose open handle the caller passes in (captured under mu; the
// caller must also hold writeMu or otherwise guarantee the file's
// contents are final: rotation or Close). The scan validates each
// entry's checksum, so a hint is only ever published for a file whose
// entries all verify. The hint is written to a temporary name, synced,
// then renamed into place, so a crash can leave at worst a missing or
// stale-cursor hint — never a half-written one that looks valid.
func (e *Engine) writeHintFile(id uint32, data *os.File) error {
	if data == nil {
		return fmt.Errorf("engine: data file %d not open for hint write", id)
	}
	info, err := data.Stat()
	if err != nil {
		return fmt.Errorf("engine: stat data file %d for hint write: %w", id, err)
	}

	tmpPath := filepath.Join(e.dir, hintFileName(id)+hintTmpSuffix)
	if err := streamHintToTmp(tmpPath, data, info.Size()); err != nil {
		return err
	}

	hintPath := filepath.Join(e.dir, hintFileName(id))
	if err := os.Rename(tmpPath, hintPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("engine: publish hint %q: %w", hintPath, err)
	}
	return syncDir(e.dir)
}

// streamHintToTmp creates tmpPath, streams one hint record per entry
// of the data file into it, and syncs it; the tmp file is removed on
// every failure path so no half-written hint lingers under the tmp
// name.
func streamHintToTmp(tmpPath string, data *os.File, size int64) error {
	tmp, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("engine: create hint tmp %q: %w", tmpPath, err)
	}
	abort := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}

	if err := writeHintRecords(tmp, data, size); err != nil {
		return abort(err)
	}
	if err := tmp.Sync(); err != nil {
		return abort(fmt.Errorf("engine: sync hint tmp %q: %w", tmpPath, err))
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("engine: close hint tmp %q: %w", tmpPath, err)
	}
	return nil
}

// writeHintRecords streams one hint record per complete valid entry of
// the data file into w. Any torn or checksum-failing region aborts the
// write: a hint must not silently omit entries.
func writeHintRecords(w *os.File, data *os.File, size int64) error {
	bw := bufio.NewWriter(w)

	var off int64
	header := make([]byte, entryHeaderSize)
	for off < size {
		remaining := size - off
		if remaining < entryHeaderSize {
			return fmt.Errorf("engine: data file has torn tail at offset %d: %w", off, ErrCorrupted)
		}
		if _, err := data.ReadAt(header, off); err != nil {
			return fmt.Errorf("engine: read header at offset %d for hint: %w", off, err)
		}
		h := decodeHeader(header)
		total := entrySizeFromHint(h.keyLen, h.valueLen)
		if total > remaining {
			return fmt.Errorf("engine: data entry at offset %d overruns file: %w", off, ErrCorrupted)
		}

		entry := make([]byte, total)
		if _, err := data.ReadAt(entry, off); err != nil {
			return fmt.Errorf("engine: read entry at offset %d for hint: %w", off, err)
		}
		if entryCRC(entry) != h.crc {
			return fmt.Errorf("engine: data entry at offset %d fails checksum: %w", off, ErrCorrupted)
		}

		key := entry[entryHeaderSize : entryHeaderSize+int(h.keyLen)]
		if err := writeHintRecord(bw, off, key, h.valueLen); err != nil {
			return err
		}
		off += total
	}
	if err := bw.Flush(); err != nil {
		return fmt.Errorf("engine: flush hint writer: %w", err)
	}
	return nil
}

// writeHintRecord appends one hint record to w: the fixed header (crc,
// keyLen, valueLen, offset) followed by the key. valueLen carries the
// entry's value length or the tombstone sentinel.
func writeHintRecord(w *bufio.Writer, offset int64, key []byte, valueLen uint32) error {
	rec := make([]byte, hintRecordHeaderSize)
	binary.LittleEndian.PutUint32(rec[hintKeyLenOff:], uint32(len(key)))
	binary.LittleEndian.PutUint32(rec[hintValueLenOff:], valueLen)
	binary.LittleEndian.PutUint64(rec[hintOffsetOff:], uint64(offset))
	binary.LittleEndian.PutUint32(rec[0:], hintRecordCRC(rec, key))
	if _, err := w.Write(rec); err != nil {
		return fmt.Errorf("engine: write hint record header: %w", err)
	}
	if _, err := w.Write(key); err != nil {
		return fmt.Errorf("engine: write hint record key: %w", err)
	}
	return nil
}

// hintRecord is one decoded hint record awaiting application to the
// index. Records are validated in full before any of them is applied,
// so a corrupt hint can never leave a half-applied index behind.
type hintRecord struct {
	key       string
	offset    int64
	size      uint32
	tombstone bool
}

// loadFromHint reads the hint file for data file id and, when every
// record validates, applies the records to the index in order and
// returns the data-file offset the hint covers (the end of its last
// record's entry) plus the number of records applied. The caller must
// then scan the data file from that offset onward — normally nothing,
// but a hint written at an earlier Close leaves the bytes appended
// after it to the scan, and a torn tail there is truncated as usual.
//
// ok is false whenever the hint cannot be trusted: missing, empty,
// structurally invalid, checksum-failing, or pointing outside the data
// file. A hint never raises an error — it is an accelerator, not a
// correctness dependency, so every failure mode falls back to scanning
// the data file from offset 0.
func (e *Engine) loadFromHint(id uint32, dataSize int64) (covered int64, records int, ok bool) {
	f, err := os.Open(filepath.Join(e.dir, hintFileName(id)))
	if err != nil {
		return 0, 0, false
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return 0, 0, false
	}

	recs, valid := parseHintRecords(f, info.Size(), e.maxKeySize, dataSize)
	if !valid {
		return 0, 0, false
	}

	for _, r := range recs {
		if r.tombstone {
			e.dropKey(r.key)
		} else {
			e.applyEntry(r.key, entryRef{fileID: id, offset: r.offset, size: r.size})
		}
	}
	if n := len(recs); n > 0 {
		last := recs[n-1]
		covered = last.offset + int64(last.size)
	}
	return covered, len(recs), true
}

// parseHintRecords decodes and validates every record of a hint file.
// ok is false when any record is torn, checksum-failing, malformed, or
// points outside the data file — the whole hint is then unusable.
func parseHintRecords(f *os.File, size int64, maxKeySize int, dataSize int64) ([]hintRecord, bool) {
	var records []hintRecord
	var off int64
	header := make([]byte, hintRecordHeaderSize)
	for off < size {
		remaining := size - off
		if remaining < hintRecordHeaderSize {
			return nil, false // torn record
		}
		if _, err := f.ReadAt(header, off); err != nil {
			return nil, false
		}
		crc := binary.LittleEndian.Uint32(header)
		keyLen := binary.LittleEndian.Uint32(header[hintKeyLenOff:])
		valueLen := binary.LittleEndian.Uint32(header[hintValueLenOff:])
		entryOffset := int64(binary.LittleEndian.Uint64(header[hintOffsetOff:]))

		if int64(keyLen) > int64(maxKeySize) {
			return nil, false // impossible key length: garbage
		}
		if int64(hintRecordHeaderSize)+int64(keyLen) > remaining {
			return nil, false // key overruns the hint file
		}
		key := make([]byte, keyLen)
		if _, err := f.ReadAt(key, off+hintRecordHeaderSize); err != nil {
			return nil, false
		}
		if hintRecordCRC(header, key) != crc {
			return nil, false
		}

		entrySize := entrySizeFromHint(keyLen, valueLen)
		if entryOffset < 0 || entryOffset+entrySize > dataSize {
			return nil, false // points outside the data file: mismatched hint
		}

		records = append(records, hintRecord{
			key:       string(key),
			offset:    entryOffset,
			size:      uint32(entrySize),
			tombstone: valueLen == tombstoneValueLen,
		})
		off += hintRecordHeaderSize + int64(keyLen)
	}
	return records, true
}
