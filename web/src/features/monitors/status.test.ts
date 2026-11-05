import { describe, expect, test } from 'vitest';
import { statusLabel, statusVariant } from './status';

describe('statusLabel', () => {
	test.each([
		['up', 'Up'],
		['down', 'Down'],
		['unknown', 'Unknown'],
		['paused', 'Paused'],
	] as const)('labels %s as %s', (status, label) => {
		expect(statusLabel(status)).toBe(label);
	});
});

describe('statusVariant', () => {
	test.each([
		['up', 'default'],
		['down', 'destructive'],
		['unknown', 'outline'],
		['paused', 'secondary'],
	] as const)('maps %s to the %s badge variant', (status, variant) => {
		expect(statusVariant(status)).toBe(variant);
	});
});
