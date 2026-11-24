import { describe, expect, test } from 'vitest';
import { screen } from '@testing-library/react';
import { renderWithProviders } from '@/test/render';
import { MCP_DOCS_URL, MCPCard } from './MCPCard';

describe('MCPCard', () => {
	test('shows the MCP endpoint and the key-role rule', () => {
		renderWithProviders(<MCPCard endpoint="https://o1.example.com/mcp" />);

		expect(screen.getByText('https://o1.example.com/mcp')).toBeInTheDocument();
		expect(screen.getByText(/viewer key/i)).toBeInTheDocument();
		expect(screen.getByText(/member key/i)).toBeInTheDocument();
	});

	test('links to the MCP docs', () => {
		renderWithProviders(<MCPCard endpoint="https://o1.example.com/mcp" />);

		expect(screen.getByRole('link', { name: /mcp docs/i })).toHaveAttribute(
			'href',
			MCP_DOCS_URL,
		);
		expect(screen.getByRole('link', { name: /mcp docs/i })).toHaveAttribute(
			'target',
			'_blank',
		);
	});
});
