import pytest

from pitwall.dlq import Policy, next_retry

POLICY = Policy(max_attempts=5, base_ms=1000, multiplier=2, max_ms=60000)


@pytest.mark.parametrize(
    "prior,want_park,want_delay,want_next",
    [
        (0, False, 1000, 1),
        (1, False, 2000, 2),
        (2, False, 4000, 3),
        (3, False, 8000, 4),
        (4, True, 0, 0),  # attempts_made = 5 == max_attempts -> park
    ],
)
def test_next_retry_backoff_then_park(prior, want_park, want_delay, want_next):
    got = next_retry(prior, POLICY)
    assert got.park == want_park
    if not want_park:
        assert got.delay_ms == want_delay
        assert got.next_retries == want_next


def test_next_retry_delay_clamped_to_max():
    p = Policy(max_attempts=100, base_ms=1000, multiplier=10, max_ms=5000)
    assert next_retry(3, p).delay_ms == 5000
