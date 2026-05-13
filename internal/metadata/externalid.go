package metadata

import (
	"errors"
	"strings"
)

var ErrBadExternalID = errors.New("external id missing source prefix")

func FormatExternalID(source, nativeID string) string {
	return source + ":" + nativeID
}

func ParseExternalID(id string) (source, nativeID string, err error) {
	i := strings.IndexByte(id, ':')
	if i <= 0 || i == len(id)-1 {
		return "", "", ErrBadExternalID
	}
	return id[:i], id[i+1:], nil
}
