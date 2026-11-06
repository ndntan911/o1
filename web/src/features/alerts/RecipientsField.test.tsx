import { useState } from 'react';
import { describe, expect, test } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { RecipientsField } from './RecipientsField';

interface HarnessProps {
	initial?: string[];
	disabled?: boolean;
}

function Harness({ initial = [], disabled = false }: HarnessProps) {
	const [recipients, setRecipients] = useState(initial);

	return (
		<RecipientsField
			recipients={recipients}
			onChange={setRecipients}
			disabled={disabled}
		/>
	);
}

describe('RecipientsField', () => {
	test('adds an address with Enter and removes it with its remove button', async () => {
		const user = userEvent.setup();

		render(<Harness initial={['ops@example.com']} />);

		expect(screen.getByText('ops@example.com')).toBeInTheDocument();

		const input = screen.getByLabelText('Add recipient');
		await user.type(input, 'dev@example.com{Enter}');

		expect(screen.getByText('dev@example.com')).toBeInTheDocument();

		await user.click(
			screen.getByRole('button', { name: 'Remove ops@example.com' }),
		);

		expect(screen.queryByText('ops@example.com')).not.toBeInTheDocument();
		expect(screen.getByText('dev@example.com')).toBeInTheDocument();
	});

	test('adds an address with the Add button and normalizes it', async () => {
		const user = userEvent.setup();

		render(<Harness />);

		await user.type(
			screen.getByLabelText('Add recipient'),
			'  Ops@Example.com  ',
		);
		await user.click(screen.getByRole('button', { name: 'Add' }));

		expect(screen.getByText('ops@example.com')).toBeInTheDocument();
		expect(screen.getByLabelText('Add recipient')).toHaveValue('');
	});

	test('rejects an invalid address', async () => {
		const user = userEvent.setup();

		render(<Harness />);

		await user.type(screen.getByLabelText('Add recipient'), 'not-an-email');
		await user.click(screen.getByRole('button', { name: 'Add' }));

		expect(screen.getByText('Enter a valid email address')).toBeInTheDocument();
	});

	test('rejects a duplicate address', async () => {
		const user = userEvent.setup();

		render(<Harness initial={['ops@example.com']} />);

		await user.type(screen.getByLabelText('Add recipient'), 'OPS@example.com');
		await user.click(screen.getByRole('button', { name: 'Add' }));

		expect(
			screen.getByText('This recipient is already added'),
		).toBeInTheDocument();
		expect(screen.getAllByText('ops@example.com')).toHaveLength(1);
	});

	test('rejects more than 20 recipients', async () => {
		const user = userEvent.setup();
		const initial = Array.from(
			{ length: 20 },
			(_, index) => `person${index}@example.com`,
		);

		render(<Harness initial={initial} />);

		await user.type(
			screen.getByLabelText('Add recipient'),
			'person20@example.com{Enter}',
		);

		expect(
			screen.getByText('You can add up to 20 recipients'),
		).toBeInTheDocument();
	});

	test('disables editing when requested', () => {
		render(<Harness initial={['ops@example.com']} disabled />);

		expect(screen.getByLabelText('Add recipient')).toBeDisabled();
		expect(screen.getByRole('button', { name: 'Add' })).toBeDisabled();
		expect(
			screen.getByRole('button', { name: 'Remove ops@example.com' }),
		).toBeDisabled();
	});
});
