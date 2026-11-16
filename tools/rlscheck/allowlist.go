package rlscheck

import "github.com/BenjaminSRussell/passage/platform/rlsexempt"

// The allowlist lives in platform/go/rlsexempt so services/ops provisioning reads the same one.
const replayNotForcedReason = rlsexempt.ReplayNotForcedReason

var allowlist = rlsexempt.Allowlist

func allowReason(v violation) (string, bool) { return rlsexempt.Reason(v.Service, v.Table, v.Problem) }
