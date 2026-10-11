package store

import "github.com/pragmataW/graphmaster/internal/model"

// Column values are written from the model enums, so narrowing back is safe;
// these helpers keep the conversions in one place.

func langOf(v int) model.Lang         { return model.Lang(uint8(v)) }
func statusOf(v int) model.FileStatus { return model.FileStatus(uint8(v)) }
func nodeKindOf(v int) model.NodeKind { return model.NodeKind(uint8(v)) }
func defFlagsOf(v int) model.DefFlags { return model.DefFlags(uint16(v)) }
