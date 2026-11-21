// Package selection decides which file of a release to install on this
// machine. It reads release files by name, removes the ones that cannot run
// here, ranks the rest, and keeps the choice sticky across versions as a
// structured Intent. It does no I/O of its own except ValidateRunnablePayload,
// which inspects a file it is given, and takes the machine as an argument.
//
// Parts are adapted from the bin project's asset resolver; see NOTICE.md.
package selection
