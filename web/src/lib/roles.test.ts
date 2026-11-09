import { expect, test } from 'vitest';
import {
	assignableRoles,
	canManageAPIKeys,
	canManageMembers,
	canManageMonitors,
	canManageStatusPage,
	roleAtLeast,
} from './roles';

test('role hierarchy', () => {
	expect(roleAtLeast('admin', 'member')).toBe(true);
	expect(roleAtLeast('viewer', 'member')).toBe(false);
	expect(canManageMembers('admin')).toBe(true);
	expect(canManageMembers('member')).toBe(false);
	expect(canManageMonitors('viewer')).toBe(false);
	expect(canManageMonitors('member')).toBe(true);
});

test('only admins and owners manage the status page', () => {
	expect(canManageStatusPage('admin')).toBe(true);
	expect(canManageStatusPage('owner')).toBe(true);
	expect(canManageStatusPage('member')).toBe(false);
	expect(canManageStatusPage('viewer')).toBe(false);
});

test('only admins and owners manage API keys', () => {
	expect(canManageAPIKeys('admin')).toBe(true);
	expect(canManageAPIKeys('owner')).toBe(true);
	expect(canManageAPIKeys('member')).toBe(false);
	expect(canManageAPIKeys('viewer')).toBe(false);
});

test('assignable roles never include owner', () => {
	expect(assignableRoles('owner')).toEqual(['viewer', 'member', 'admin']);
	expect(assignableRoles('admin')).toEqual(['viewer', 'member']);
	expect(assignableRoles('member')).toEqual([]);
	expect(assignableRoles('viewer')).toEqual([]);
});
