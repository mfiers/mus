"""
Test the hooks system
"""
import pytest

from mus.hooks import HOOKS, call_hook, register_hook


@pytest.fixture
def clean_hooks():
    """Clear hooks before and after each test"""
    HOOKS.clear()
    yield
    HOOKS.clear()


def test_register_hook(clean_hooks):
    """Test registering a hook"""
    def test_function():
        pass

    register_hook('test_hook', test_function)
    assert 'test_hook' in HOOKS
    assert len(HOOKS['test_hook']) == 1
    assert HOOKS['test_hook'][0][1] == test_function


def test_register_multiple_hooks(clean_hooks):
    """Test registering multiple hooks with the same name"""
    def func1():
        pass

    def func2():
        pass

    register_hook('multi_hook', func1)
    register_hook('multi_hook', func2)

    assert len(HOOKS['multi_hook']) == 2


def test_hook_priority(clean_hooks):
    """Test that hooks are called in priority order"""
    call_order = []

    def low_priority():
        call_order.append('low')

    def high_priority():
        call_order.append('high')

    def medium_priority():
        call_order.append('medium')

    register_hook('priority_test', low_priority, priority=1)
    register_hook('priority_test', high_priority, priority=20)
    register_hook('priority_test', medium_priority, priority=10)

    call_hook('priority_test')

    assert call_order == ['high', 'medium', 'low']


def test_call_hook_with_no_registered_hooks(clean_hooks):
    """Test calling a hook that doesn't exist"""
    # Should not raise an error
    call_hook('nonexistent_hook')


def test_call_hook_with_arguments(clean_hooks):
    """Test calling hooks with keyword arguments"""
    result = []

    def hook_with_args(data_name, data_value):
        result.append((data_name, data_value))

    register_hook('args_hook', hook_with_args)
    call_hook('args_hook', data_name='test', data_value=42)

    assert result == [('test', 42)]


def test_multiple_hooks_with_arguments(clean_hooks):
    """Test multiple hooks receiving the same arguments"""
    results = []

    def hook1(data):
        results.append(f"hook1: {data}")

    def hook2(data):
        results.append(f"hook2: {data}")

    register_hook('multi_args', hook1, priority=10)
    register_hook('multi_args', hook2, priority=5)

    call_hook('multi_args', data='test_data')

    assert len(results) == 2
    assert results[0] == "hook1: test_data"
    assert results[1] == "hook2: test_data"


def test_hook_default_priority(clean_hooks):
    """Test that default priority is 10"""
    call_order = []

    def default_func():
        call_order.append('default')

    def high_func():
        call_order.append('high')

    register_hook('default_priority', default_func)  # Should be priority 10
    register_hook('default_priority', high_func, priority=20)

    call_hook('default_priority')

    assert call_order == ['high', 'default']


def test_hook_modifies_shared_state(clean_hooks):
    """Test that hooks can modify shared state"""
    state = {'counter': 0}

    def increment(state_dict):
        state_dict['counter'] += 1

    def double(state_dict):
        state_dict['counter'] *= 2

    register_hook('modify_state', increment, priority=10)
    register_hook('modify_state', double, priority=5)

    call_hook('modify_state', state_dict=state)

    # increment runs first (priority 10), then double (priority 5)
    # 0 + 1 = 1, then 1 * 2 = 2
    assert state['counter'] == 2


def test_hook_with_record_object(clean_hooks):
    """Test hooks working with Record-like objects"""
    class MockRecord:
        def __init__(self):
            self.data = {}

    def add_metadata(record):
        record.data['hook_added'] = True

    register_hook('prepare_record', add_metadata)

    record = MockRecord()
    call_hook('prepare_record', record=record)

    assert record.data['hook_added'] is True


def test_plugin_init_hook_pattern(clean_hooks):
    """Test the plugin_init hook pattern used in the codebase"""
    initialized_plugins = []

    def init_plugin_a(cli):
        initialized_plugins.append('plugin_a')

    def init_plugin_b(cli):
        initialized_plugins.append('plugin_b')

    register_hook('plugin_init', init_plugin_a)
    register_hook('plugin_init', init_plugin_b)

    # Simulate calling plugin_init with a mock CLI
    class MockCLI:
        pass

    call_hook('plugin_init', cli=MockCLI())

    assert len(initialized_plugins) == 2
    assert 'plugin_a' in initialized_plugins
    assert 'plugin_b' in initialized_plugins


def test_hook_exception_propagation(clean_hooks):
    """Test that exceptions in hooks propagate correctly"""
    def failing_hook():
        raise ValueError("Hook failed")

    register_hook('failing_hook', failing_hook)

    with pytest.raises(ValueError, match="Hook failed"):
        call_hook('failing_hook')


def test_hook_priority_ties(clean_hooks):
    """Test hooks with the same priority"""
    call_order = []

    def func1():
        call_order.append('func1')

    def func2():
        call_order.append('func2')

    register_hook('same_priority', func1, priority=10)
    register_hook('same_priority', func2, priority=10)

    call_hook('same_priority')

    # Both should be called, order depends on registration order
    assert len(call_order) == 2
    assert 'func1' in call_order
    assert 'func2' in call_order
