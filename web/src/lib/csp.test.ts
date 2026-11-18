import { describe, expect, test } from 'vitest';
import { z } from 'zod';
import './csp';

describe('csp', () => {
	test('disables zod JIT so the policy does not need unsafe-eval', () => {
		const core = z.core as { globalConfig?: { jitless?: boolean } };
		expect(core.globalConfig?.jitless).toBe(true);
	});
});
