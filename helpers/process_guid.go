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

func addPadding(s string) string {
	for len(s)%8 != 0 {
		s = s + "="
	}

	return s
}

func DecodeProcessGuid(shortenedGuid string) (string, error) {
	splited := strings.Split(shortenedGuid, "-")
	if len(splited) != 2 {
		return "", errors.New("invalid shortened process guid")
	}
	// add padding
	appGuid := addPadding(strings.ToUpper(splited[0]))
	appVersion := addPadding(strings.ToUpper(splited[1]))

	// decode it
	longAppGuid, err := base32.StdEncoding.DecodeString(appGuid[:])
	if err != nil {
		return "", errors.New("Unable to decode appGuid - invalid shortened process guid")
	}
	longAppVersion, err := base32.StdEncoding.DecodeString(appVersion[:])
	if err != nil {
		return "", errors.New("Unable to decode appVersion - invalid shortened process guid")
	}

	appGuidUUID, err := uuid.Parse(longAppGuid)
	appVersionUUID, err := uuid.Parse(longAppVersion)

	if err != nil {
		return "", errors.New("Unable to parse appGuid - invalid shortened process guid")
	}

	if err != nil {
		return "", errors.New("Unable to parse appVersion - invalid shortened process guid")
	}

	return appGuidUUID.String() + "-" + appVersionUUID.String(), nil
}
