import { expect, test } from 'vitest';
import { render, screen } from '@testing-library/react';
import { Button } from '@/components/ui/button';

test('renders the shared button primitive', () => {
	render(<Button>Save</Button>);
	expect(screen.getByRole('button', { name: 'Save' })).toBeInTheDocument();
});
