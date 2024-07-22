
import logging


class ColorFormatter(logging.Formatter):
    # Change this dictionary to suit your coloring needs!

    def format(self, record):

        # prevents import unless used
        from colorama import Back, Fore

        from mus.util import msec2nice

        colors = {
            "WARNING": Fore.RED,
            "ERROR": Fore.RED + Back.WHITE,
            "DEBUG": Fore.LIGHTBLACK_EX,
            "INFO": Fore.GREEN,
            "CRITICAL": Fore.RED + Back.WHITE
        }

        level_short = dict(
            WARNING='W',
            ERROR='E',
            INFO='I',
            DEBUG='D',
            CRITICAL='C'
        )

        rc = record.relativeCreated
        record.relativeCreatedStr = '(' + msec2nice(rc) + ')'

        color = colors.get(record.levelname, "")
        record.levelShort = color + level_short[record.levelname]

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
