
import sys

from click.testing import CliRunner

import mus.cli


def run_macro(macro):
    runner = CliRunner(mix_stderr=False)
    result = runner.invoke(mus.cli.cli,
                           ['macro', 'stdin-exe'],
                           input=' 1111  m ' + macro)
    return result


def test_run_macro_ls():
    "Test"
    result = run_macro('ls test/data')
    assert 'test01.txt' in result.output
    assert result.exit_code == 0
