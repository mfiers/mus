import logging

lg = logging.getLogger("mus")


class Executor():
    """Base class of all executors."""
    def __init__(self, no_threads, force: bool = False):
        self.no_threads = no_threads
        self.force = force

    def execute(self,
                jobiterator,
                max_no_jobs: int = -1):
        raise NotImplementedError('')

class SimpleExecutor(Executor):
    """Execute in order, not parallel."""
    def execute(self,
                jobiterator,
                max_no_jobs: int = -1):

        import subprocess as sp
        import sys
        if max_no_jobs == -1:
            max_no_jobs = 10000000000

        def run_one(job):
            lg.info(f"Executing {job.record.uid}: {job.cl}")
            job.start()
            P = sp.Popen(
                job.cl, stdout=sp.PIPE, stderr=sp.PIPE)
            out, err = P.communicate()
            sys.stdout.write(out.decode(encoding='utf-8'))
            sys.stderr.write(err.decode(encoding='utf-8'))
            job.stop(P.returncode)
            lg.debug(f"Finished {job.record.uid}: {job.cl}")

        for job in jobiterator():
            run_one(job)


class AsyncioExecutor(Executor):

    def execute(self,
                jobiterator,
                max_no_jobs: int = -1):

        import asyncio
        import subprocess as sp
        import sys

        all_jobs = list(jobiterator())

        lg.info("starting jobs")
        [x.start() for x in all_jobs]

        async def run_all():

            # to ensure the max no subprocesses
            sem = asyncio.Semaphore(self.no_threads)

            async def run_one(job):
                async with sem:
                    lg.info(f"Executing {job.record.uid}: {job.cl}")
                    P = await asyncio.create_subprocess_shell(
                        job.cl, stdout=sp.PIPE, stderr=sp.PIPE)
                    out, err = await P.communicate()
                    job.set_returncode(P.returncode)

                    sys.stdout.write(out.decode(encoding='utf-8'))
                    sys.stderr.write(err.decode(encoding='utf-8'))
                    lg.debug(f"Finished {job.record.uid}: {job.cl}")

            async def run_all():
                await asyncio.gather(
                    *(run_one(job)
                      for (i, job)
                      in enumerate(all_jobs)))

            await run_all()

        asyncio.run(run_all())

        lg.info("Finishing up jobs")
        [x.stop() for x in all_jobs]
