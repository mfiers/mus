
import logging


class ColorFormatter(logging.Formatter):
    # Change this dictionary to suit your coloring needs!
    from colorama import Back, Fore

    COLORS = {
        "WARNING": Fore.RED,
        "ERROR": Fore.RED + Back.WHITE,
        "DEBUG": Fore.LIGHTBLACK_EX,
        "INFO": Fore.GREEN,
        "CRITICAL": Fore.RED + Back.WHITE
    }
    LEVEL_SHORT = dict(
        WARNING='W',
        ERROR='E',
        INFO='I',
        DEBUG='D',
        CRITICAL='C')

    def format(self, record):

        from colorama import Back, Fore

        rc = record.relativeCreated
        record.relativeCreatedStr = '(' + msec2nice(rc) + ')'

        color = self.COLORS.get(record.levelname, "")
        record.levelShort = color + self.LEVEL_SHORT[record.levelname]

        if color:
            record.name = color + record.name
            record.msg = color + record.msg
            record.relativeCreatedStr = \
                Fore.LIGHTBLACK_EX + record.relativeCreatedStr
        return logging.Formatter.format(self, record)


class ColorLogger(logging.Logger):
    def __init__(self, name):
        logging.Logger.__init__(self, name)
        color_formatter = ColorFormatter(
            "%(levelShort)s %(message)s %(relativeCreatedStr)s",
        )
        console = logging.StreamHandler()
        console.setFormatter(color_formatter)
        self.addHandler(console)


def msec2nice(mtime):

    if mtime < 1000:
        return f"{mtime:.0f}ms"
    elif mtime < 60 * 1000:
        # seconds
        return f"{mtime / 1000:02.1f}s"
    # elif mtime < 5 * 60 * 1000:
    #     # minutes+seconds when smaller than 5 minutes
    #     allseconds = int(mtime // 1000)
    #     minutes = allseconds // 60
    #     seconds = allseconds % 60
    #     return f"{minutes}m:{seconds:02d}s"
    elif mtime < 60 * 60 * 1000:
        # minutes when smaller than 60 minutes
        allseconds = int(mtime // 1000)
        minutes = allseconds // 60
        seconds = allseconds % 60
        return f"{minutes}m:{seconds:02d}s"
    elif mtime < 60 * 60 * 1000 * 24:
        allminutes = int(mtime // (1000 * 60))
        hours = allminutes // 60
        minutes = allminutes % 60
        return f"{hours}h:{minutes:02d}m"
    else:
        allhours = int(mtime // (1000 * 60 * 60))
        days = allhours // 24
        hours = allhours % 24
        return f"{days}d:{hours:02d}h"
