package helpers

import (
	"encoding/base32"
	"errors"
	"strings"

	"github.com/nu7hatch/gouuid"
)

type ProcessGuid struct {
	AppGuid    *uuid.UUID
	AppVersion *uuid.UUID
}

func NewProcessGuid(guid string) (ProcessGuid, error) {
	if len(guid) < 36 {
		return ProcessGuid{}, errors.New("invalid process guid")
	}

	appGuid, err := uuid.ParseHex(guid[:36])
	if err != nil {
		return ProcessGuid{}, err
	}

	appVersion, err := uuid.ParseHex(guid[37:])
	if err != nil {
		return ProcessGuid{}, err
	}

	return ProcessGuid{
		AppGuid:    appGuid,
		AppVersion: appVersion,
	}, nil
}

func (pg ProcessGuid) ShortenedGuid() string {
	shortAppGuid := trimPadding(base32.StdEncoding.EncodeToString(pg.AppGuid[:]))
	shortAppVersion := trimPadding(base32.StdEncoding.EncodeToString(pg.AppVersion[:]))

	return strings.ToLower(shortAppGuid + "-" + shortAppVersion)
}

func (pg ProcessGuid) String() string {
	return pg.AppGuid.String() + "-" + pg.AppVersion.String()
}

func trimPadding(s string) string {
	return strings.TrimRight(s, "=")
}
