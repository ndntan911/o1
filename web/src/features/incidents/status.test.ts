import { describe, expect, test } from 'vitest';
import { incidentEventLabel } from './status';

describe('incidentEventLabel', () => {
	test('labels every incident event kind', () => {
		expect(incidentEventLabel('opened')).toBe('Opened');
		expect(incidentEventLabel('acknowledged')).toBe('Acknowledged');
		expect(incidentEventLabel('resolved')).toBe('Resolved');
		expect(incidentEventLabel('investigation_completed')).toBe('Investigated');
	});
});
