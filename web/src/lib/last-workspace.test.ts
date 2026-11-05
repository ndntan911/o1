import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import {
	LAST_WORKSPACE_KEY,
	readLastWorkspaceSlug,
	writeLastWorkspaceSlug,
} from './last-workspace';

beforeEach(() => {
	localStorage.clear();
});

afterEach(() => {
	vi.restoreAllMocks();
});

test('a missing key returns null', () => {
	expect(readLastWorkspaceSlug()).toBeNull();
});

test('a written slug round-trips', () => {
	writeLastWorkspaceSlug('acme');
	expect(readLastWorkspaceSlug()).toBe('acme');
	expect(localStorage.getItem(LAST_WORKSPACE_KEY)).toBe('acme');
});

test('storage failures do not throw', () => {
	vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
		throw new Error('blocked');
	});
	vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
		throw new Error('blocked');
	});

	expect(readLastWorkspaceSlug()).toBeNull();
	expect(() => writeLastWorkspaceSlug('acme')).not.toThrow();
});
