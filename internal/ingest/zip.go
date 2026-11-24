package ingest

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"math"
)

const (
	zipEndSignature        = "PK\x05\x06"
	zipDirectorySignature  = "PK\x01\x02"
	zip64LocatorSignature  = "PK\x06\x07"
	zip64EndSignature      = "PK\x06\x06"
	zipEndSize             = 22
	zipDirectoryHeaderSize = 46
	zip64LocatorSize       = 20
	zip64EndSize           = 56
	// Match the standard library's search window to preflight the same EOCD.
	zipEndSearchBytes = 65 << 10
	zipCountOffset    = 10
	zipSizeOffset     = 12
	zipStartOffset    = 16
)

// checkZipDirectory bounds how many entries archive/zip can allocate. The
// standard reader walks actual directory headers even when their declared
// count wraps or the count is smaller than the number of headers.
func checkZipDirectory(data []byte) error {
	start := max(0, len(data)-zipEndSearchBytes)
	tail := data[start:]

	index := lastZipEnd(tail)
	if index < 0 {
		return zip.ErrFormat
	}

	directoryEnd := start + index

	count, size, offset := zipDirectoryFields(data, &directoryEnd)
	if count > MaxZipEntries {
		return errZipEntries
	}

	if size > uint64(directoryEnd) || offset > uint64(len(data)) {
		return zip.ErrFormat
	}

	directoryStart := uint64(directoryEnd) - size

	err := countZipHeaders(data[directoryStart:])
	if err != nil {
		return err
	}
	// Archive/zip may fall back to the declared offset for prefixed ZIPs.
	if offset < directoryStart {
		return countZipHeaders(data[offset:])
	}

	return nil
}

func lastZipEnd(tail []byte) int {
	for pos := len(tail) - zipEndSize; pos >= 0; pos-- {
		if !bytes.HasPrefix(tail[pos:], []byte(zipEndSignature)) {
			continue
		}

		comment := binary.LittleEndian.Uint16(tail[pos+zipEndSize-2:])
		if int(comment) <= len(tail)-pos-zipEndSize {
			return pos
		}

		return -1
	}

	return -1
}

func zipDirectoryFields(data []byte, endOffset *int) (uint64, uint64, uint64) {
	const (
		count64Offset = 32
		size64Offset  = 40
		start64Offset = 48
	)

	end := data[*endOffset:]
	count := uint64(binary.LittleEndian.Uint16(end[zipCountOffset:]))
	size := uint64(binary.LittleEndian.Uint32(end[zipSizeOffset:]))
	offset := uint64(binary.LittleEndian.Uint32(end[zipStartOffset:]))

	if pos := zip64EndOffset(data, *endOffset); pos >= 0 {
		*endOffset = pos
		end64 := data[pos:]
		count = binary.LittleEndian.Uint64(end64[count64Offset:])
		size = binary.LittleEndian.Uint64(end64[size64Offset:])
		offset = binary.LittleEndian.Uint64(end64[start64Offset:])
	}

	return count, size, offset
}

func zip64EndOffset(data []byte, endOffset int) int {
	start := endOffset - zip64LocatorSize
	if start < 0 {
		return -1
	}

	const (
		zip64Offset          = 8
		zip64DiskOffset      = 4
		zip64DiskCountOffset = 16
	)

	locator := data[start:endOffset]
	if !bytes.HasPrefix(locator, []byte(zip64LocatorSignature)) ||
		binary.LittleEndian.Uint32(locator[zip64DiskOffset:]) != 0 ||
		binary.LittleEndian.Uint32(locator[zip64DiskCountOffset:]) != 1 {
		return -1
	}

	end := data[endOffset:]
	if binary.LittleEndian.Uint16(end[zipCountOffset:]) != 0xffff &&
		binary.LittleEndian.Uint32(end[zipSizeOffset:]) != 0xffffffff &&
		binary.LittleEndian.Uint32(end[zipStartOffset:]) != 0xffffffff {
		return -1
	}

	offset := binary.LittleEndian.Uint64(locator[zip64Offset:])
	if endOffset < zip64EndSize+zip64LocatorSize || offset > math.MaxInt ||
		offset > uint64(endOffset-zip64EndSize-zip64LocatorSize) ||
		!bytes.HasPrefix(data[offset:], []byte(zip64EndSignature)) {
		return -1
	}

	return int(offset)
}

func countZipHeaders(directory []byte) error {
	const (
		fileNameOffset = 28
		extraOffset    = 30
		commentOffset  = 32
	)

	for count := 0; len(directory) >= zipDirectoryHeaderSize &&
		bytes.HasPrefix(directory, []byte(zipDirectorySignature)); count++ {
		if count == MaxZipEntries {
			return errZipEntries
		}

		filename := binary.LittleEndian.Uint16(directory[fileNameOffset:])
		extra := binary.LittleEndian.Uint16(directory[extraOffset:])
		comment := binary.LittleEndian.Uint16(directory[commentOffset:])

		size := zipDirectoryHeaderSize + int(filename) + int(extra) + int(comment)
		if size > len(directory) {
			return zip.ErrFormat
		}

		directory = directory[size:]
	}

	return nil
}
