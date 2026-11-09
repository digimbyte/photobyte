package media

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"io"

	"digiphoto/internal/document"
)

const maxProfile = 4 << 20

func pngProfile(data []byte) ([]byte, error) {
	name := bytes.IndexByte(data, 0)
	if name < 1 || name > 79 || name+2 >= len(data) || data[name+1] != 0 {
		return nil, errors.New("invalid PNG ICC profile")
	}
	r, err := zlib.NewReader(bytes.NewReader(data[name+2:]))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	profile, err := io.ReadAll(io.LimitReader(r, maxProfile+1))
	if len(profile) > maxProfile {
		return nil, errors.New("PNG color profile is too large")
	}
	return profile, err
}

func jpegProfile(data []byte) ([]byte, error) {
	data = data[2:]
	var parts [][]byte
	size := 0
	for len(data) >= 4 && data[0] == 255 {
		if data[1] == 255 {
			data = data[1:]
			continue
		}
		if data[1] == 0xda || data[1] == 0xd9 {
			break
		}
		n := int(binary.BigEndian.Uint16(data[2:]))
		if n < 2 || n > len(data)-2 {
			break
		}
		seg := data[4 : n+2]
		if data[1] == 0xe2 && bytes.HasPrefix(seg, []byte("ICC_PROFILE\x00")) {
			if len(seg) < 14 {
				return nil, errors.New("truncated JPEG ICC profile")
			}
			i, total := int(seg[12]), int(seg[13])
			if parts == nil {
				parts = make([][]byte, total)
			}
			if i < 1 || i > total || len(parts) != total || parts[i-1] != nil {
				return nil, errors.New("invalid JPEG ICC profile sequence")
			}
			parts[i-1] = seg[14:]
			size += len(parts[i-1])
			if size > maxProfile {
				return nil, errors.New("JPEG color profile is too large")
			}
		}
		data = data[n+2:]
	}
	profile := make([]byte, 0, size)
	for _, part := range parts {
		if part == nil {
			return nil, errors.New("incomplete JPEG ICC profile")
		}
		profile = append(profile, part...)
	}
	return profile, nil
}

func webpProfile(data []byte) ([]byte, error) {
	data = data[12:]
	for len(data) >= 8 {
		n := int(binary.LittleEndian.Uint32(data[4:]))
		if n > len(data)-8 {
			break
		}
		if string(data[:4]) == "ICCP" {
			if n > maxProfile {
				return nil, errors.New("WebP color profile is too large")
			}
			return data[8 : 8+n], nil
		}
		n += n % 2
		if n > len(data)-8 {
			break
		}
		data = data[8+n:]
	}
	return nil, nil
}

func prepareProfile(h *Header, profile *[]byte) error {
	if len(*profile) == 0 {
		return nil
	}
	ok, err := profileDriver()
	if err != nil {
		return err
	}
	h.Space = document.SRGB
	if ok {
		h.Warning = "Embedded color profile converted to sRGB."
	} else {
		*profile = nil
		h.Warning = "Embedded color profile was not converted. Preview assumes sRGB."
	}
	return nil
}
