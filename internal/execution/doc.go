// Package execution runs platform-authorized tasks and delivers their results.
// Engine owns admission, cancellation, process cleanup, and transfer concurrency
// across control reconnects. Results are persisted before publication; result
// acknowledgments and file delivery advance separately. Losing a control channel
// does not rerun a command or extend its original deadline.
package execution
