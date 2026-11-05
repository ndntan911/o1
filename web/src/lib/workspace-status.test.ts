import { beforeEach, expect, test } from 'vitest';
import {
	clearWorkspaceSuspended,
	getWorkspaceSuspended,
	markWorkspaceSuspended,
	resetWorkspaceStatusForTests,
} from './workspace-status';

beforeEach(() => resetWorkspaceStatusForTests());

test('suspension store toggles', () => {
	expect(getWorkspaceSuspended()).toBe(false);
	markWorkspaceSuspended();
	expect(getWorkspaceSuspended()).toBe(true);
	clearWorkspaceSuspended();
	expect(getWorkspaceSuspended()).toBe(false);
});
