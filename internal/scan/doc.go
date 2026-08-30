// Package scan implements the parallel directory crawler (specification §7).
//
// Milestone M1. Responsibilities: work-stealing directory queue, per-share
// worker pool, exclusion matching, symlink and mount-point rules, hard-link
// de-duplication, progress reporting, cancellation, subtree rescans and
// scheduling. It produces a model.Generation and never touches the HTTP layer.
package scan
