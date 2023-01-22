import logging

lg = logging.getLogger("mus")


class Executor():
    """Base class of all executors."""
    def __init__(self, no_threads):
        self.no_threads = no_threads


class AsyncioExecutor(Executor):

    def execute(self, jobiterator):

        import asyncio

        async def run_all():

            # to ensure the max no subprocesses
            sem = asyncio.Semaphore(self.no_threads)

            async def run_one(job):
                async with sem:
                    lg.info(f"Executing {job.record.uid}: {job.cl}")
                    job.start()
                    P = await asyncio.create_subprocess_shell(job.cl)
                    await P.communicate()
                    job.stop(P.returncode)
                    # self.add_to_script_log(job)
                    lg.debug(f"Finished {job.record.uid}: {job.cl}")

            async def run_all():
                await asyncio.gather(
                    *[run_one(job) for job in jobiterator()]
                )

            await run_all()

        asyncio.run(run_all())