// Package atomicfile replaces a file so a reader sees either the old content or the new,
// never half of it: it writes a temporary file beside the target, applies the mode,
// fsyncs and closes it, renames it over the target and fsyncs the parent directory.
// On Windows the directory flush is skipped, since Windows flushes no directory handle.
// On any failure the temporary file is removed.
package atomicfile
