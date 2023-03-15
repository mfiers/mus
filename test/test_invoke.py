

from click.testing import CliRunner

import mus.cli


def test_run_one():
    "Test"
    runner = CliRunner()
    result = runner.invoke(mus.cli.cli)
    assert result.exit_code == 0
    assert 'Usage: cli' in result.output


def test_run_help():
    runner = CliRunner()
    result = runner.invoke(mus.cli.cli, '--help')
    assert result.exit_code == 0
    assert 'Usage: cli' in result.output
    assert 'Commands:' in result.output