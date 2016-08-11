package bulk

import (
	"github.com/cloudfoundry-incubator/nsync/helpers"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/pivotal-golang/lager"
)

//go:generate counterfeiter -o fakes/fake_app_differ.go . AppDiffer

type AppDiffer interface {
	Diff(logger lager.Logger, cancel <-chan struct{}, fingerprints <-chan []cc_messages.CCDesiredAppFingerprint) <-chan error

	Stale() <-chan []cc_messages.CCDesiredAppFingerprint

	Missing() <-chan []cc_messages.CCDesiredAppFingerprint

	Deleted() <-chan []string
}

type appDiffer struct {
	existingSchedulingInfos map[string]*KubeSchedulingInfo

	stale   chan []cc_messages.CCDesiredAppFingerprint
	missing chan []cc_messages.CCDesiredAppFingerprint
	deleted chan []string
}

func NewAppDiffer(existing map[string]*KubeSchedulingInfo) AppDiffer {
	return &appDiffer{
		existingSchedulingInfos: copySchedulingInfoMap(existing),

		stale:   make(chan []cc_messages.CCDesiredAppFingerprint, 1),
		missing: make(chan []cc_messages.CCDesiredAppFingerprint, 1),
		deleted: make(chan []string, 1),
	}
}

func (d *appDiffer) Diff(
	logger lager.Logger,
	cancel <-chan struct{},
	fingerprints <-chan []cc_messages.CCDesiredAppFingerprint,
) <-chan error {
	logger = logger.Session("diff")

	errc := make(chan error, 1)

	go func() {
		defer func() {
			close(d.missing)
			close(d.stale)
			close(d.deleted)
			close(errc)
		}()

		for {
			select {
			case <-cancel:
				return

			case batch, open := <-fingerprints:
				if !open {
					remaining := remainingProcessGuids(d.existingSchedulingInfos)
					if len(remaining) > 0 {
						d.deleted <- remaining
					}
					return
				}

				missing := []cc_messages.CCDesiredAppFingerprint{}
				stale := []cc_messages.CCDesiredAppFingerprint{}

				for _, fingerprint := range batch {
					processGuid, err := helpers.NewProcessGuid(fingerprint.ProcessGuid)
					if err != nil {
						logger.Error("unable to create new process guid", err)
						continue
					}
					kubeSchedulingInfo, found := d.existingSchedulingInfos[processGuid.ShortenedGuid()]
					if !found {
						logger.Info("found-missing-desired-lrp", lager.Data{
							"guid": fingerprint.ProcessGuid,
							"etag": fingerprint.ETag,
						})

						missing = append(missing, fingerprint)
						continue
					}

					delete(d.existingSchedulingInfos, processGuid.ShortenedGuid())

					if kubeSchedulingInfo.ETag != fingerprint.ETag {
						logger.Info("found-stale-lrp", lager.Data{
							"guid": fingerprint.ProcessGuid,
							"etag": fingerprint.ETag,
						})

						stale = append(stale, fingerprint)
					}
				}

				if len(missing) > 0 {
					select {
					case d.missing <- missing:
					case <-cancel:
						return
					}
				}

				if len(stale) > 0 {
					select {
					case d.stale <- stale:
					case <-cancel:
						return
					}
				}
			}
		}
	}()

	return errc
}

func copySchedulingInfoMap(schedulingInfoMap map[string]*KubeSchedulingInfo) map[string]*KubeSchedulingInfo {
	clone := map[string]*KubeSchedulingInfo{}
	for k, v := range schedulingInfoMap {
		clone[k] = v
	}
	return clone
}

func remainingProcessGuids(remaining map[string]*KubeSchedulingInfo) []string {
	keys := make([]string, 0, len(remaining))
	for _, schedulingInfo := range remaining {
		keys = append(keys, schedulingInfo.ProcessGuid)
	}

	return keys
}

func (d *appDiffer) Stale() <-chan []cc_messages.CCDesiredAppFingerprint {
	return d.stale
}

func (d *appDiffer) Missing() <-chan []cc_messages.CCDesiredAppFingerprint {
	return d.missing
}

func (d *appDiffer) Deleted() <-chan []string {
	return d.deleted
}
