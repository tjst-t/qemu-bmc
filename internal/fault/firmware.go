package fault

// matchFirmware reports whether a firmware-* fault applies to an update of
// the firmware component target (BMC or BIOS).
func (f *Fault) matchFirmware(target string) bool {
	return f.Target == "firmware" || f.Target == target
}

// RejectFirmwareImage reports whether a firmware-image-rejected fault
// rejects a received image for target as if it were broken. A nil store
// rejects none.
func (s *Store) RejectFirmwareImage(target string) bool {
	return s.take(func(f *Fault) bool {
		return f.Kind == FirmwareImageRejected && f.matchFirmware(target)
	}, "firmware image for "+target) != nil
}

// FirmwareUpdateFailure returns the PercentComplete at which a
// firmware-update-fail fault makes an update of target fail, and false if
// no fault applies. A nil store fails none.
func (s *Store) FirmwareUpdateFailure(target string) (atPercent int, ok bool) {
	e := s.take(func(f *Fault) bool {
		return f.Kind == FirmwareUpdateFail && f.matchFirmware(target)
	}, "firmware update of "+target)
	if e == nil {
		return 0, false
	}
	return *e.AtPercent, true
}
