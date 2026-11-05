import { describe, expect, test } from 'vitest';
import { addMemberSchema, workspaceNameSchema } from './schemas';

describe('workspaceNameSchema', () => {
	test('trims and accepts a normal name', () => {
		const result = workspaceNameSchema.safeParse('  Acme Status  ');
		expect(result.success).toBe(true);
		expect(result.data).toBe('Acme Status');
	});

	test('rejects an empty or whitespace-only name', () => {
		const result = workspaceNameSchema.safeParse('   ');
		expect(result.success).toBe(false);
		expect(result.error?.issues[0]?.message).toBe('Workspace name is required');
	});

	test('accepts 80 characters and rejects 81', () => {
		expect(workspaceNameSchema.safeParse('a'.repeat(80)).success).toBe(true);

		const result = workspaceNameSchema.safeParse('a'.repeat(81));
		expect(result.success).toBe(false);
		expect(result.error?.issues[0]?.message).toBe(
			'Workspace name must be at most 80 characters',
		);
	});
});

describe('addMemberSchema', () => {
	test('trims and lowercases the email', () => {
		const result = addMemberSchema.safeParse({
			email: '  NEW@Example.COM ',
			role: 'member',
		});
		expect(result.success).toBe(true);
		expect(result.data).toEqual({
			email: 'new@example.com',
			role: 'member',
		});
	});

	test('rejects a malformed email', () => {
		const result = addMemberSchema.safeParse({
			email: 'not-an-email',
			role: 'viewer',
		});
		expect(result.success).toBe(false);
		expect(result.error?.issues[0]?.message).toBe(
			'Enter a valid email address',
		);
	});

	test('never accepts the owner role', () => {
		expect(
			addMemberSchema.safeParse({
				email: 'owner@example.com',
				role: 'owner',
			}).success,
		).toBe(false);
	});
});
