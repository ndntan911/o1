import { expect, test } from 'vitest';
import { nextPageParam } from './pagination';

test('nextPageParam ignores absent and empty cursors', () => {
	expect(nextPageParam({})).toBeUndefined();
	expect(nextPageParam({ next_cursor: '' })).toBeUndefined();
	expect(nextPageParam({ next_cursor: 'abc' })).toBe('abc');
});
