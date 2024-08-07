

from pathlib import Path

from mus.hooks import register_hook


def prepare_job_needs_execution(job):
    """Check if this job can/should be executed.

    Expectations:
    a) all input files exist
    b) if all outputfiles exists, then at least one of them is older than the
       most recent inputfile
    """

    to_path_list = lambda fl: [Path(x) for x in fl]
    input_files = to_path_list(job.file_meta.get('input', set()))
    output_files = to_path_list(job.file_meta.get('output', set()))

    if len(input_files) == 0:
        # no reason to not run this job...
        return

    input_mtimes = []
    output_mtimes = []

    for input_file in input_files:
        if not input_file.exists():
            job.set_run_advise(False, f"Input file '{input_file}' does not exist")
        else:
            input_mtimes.append(input_file.stat().st_mtime)
    # job.set_run_advise(True, "All input files exist")

    for output_file in output_files:
        if output_file.exists():
            output_mtimes.append(output_file.stat().st_mtime)
        else:
            job.set_run_advise(True, "At least one output file does not exist")
            return


    if len(output_mtimes) == 0:
        # no output files (exist) - no reason not to run this job
        return

    if max(input_mtimes) < min(output_mtimes):
        # All output files are more recently modified than the input
        # files ... or ....
        # the most recent input file modification time (max(input_mtimes))
        # is smaller than the oldest output file mtime (min(output_mtimes))
        job.set_run_advise(False, "Output files newer than input files")


register_hook('prepare_job', prepare_job_needs_execution)
