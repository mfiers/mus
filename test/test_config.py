
import sys

import mus.config


def test_load_env():
    c = mus.config.load_env('test/data/.env')
    assert c['test'] == '1'
    assert c['test2'] == 'something'
    assert 'tag1' in c['tag']
    assert 'tag2' in c['tag']
    c = mus.config.load_env('test/data/subfolder/.env')
    assert c['test2'] == 'else'
    assert c['test3'] == 'parrot'
    assert 'test' not in c
    assert 'tag1' not in c['tag']
    assert 'tag2' not in c['tag']
    assert 'tag3' in c['tag']


def test_get_recursive_config():
    c = mus.config.get_config('test/data')
    assert c['test'] == '1'
    assert c['test2'] == 'something'
    assert 'tag1' in c['tag']
    assert 'tag2' in c['tag']
    assert 'tag3' not in c['tag']

    c = mus.config.get_config('test/data/subfolder')
    assert c['test'] == '1'
    assert c['test2'] == 'else'
    assert c['test3'] == 'parrot'
    assert 'tag1' in c['tag']
    assert 'tag2' in c['tag']
    assert 'tag3' in c['tag']
