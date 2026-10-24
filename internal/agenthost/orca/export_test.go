package orca

import "time"

// HeadReads is the number of rollout heads h read from disk.
func HeadReads(h *Host) int64 { return h.headReads.Load() }

// SetWall replaces the clock that dates a rollout's change time.
func SetWall(h *Host, wall func() time.Time) { h.wall = wall }
