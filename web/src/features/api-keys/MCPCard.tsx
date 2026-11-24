import {
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
} from '@/components/ui/card';

export const MCP_DOCS_URL = '/docs/mcp.md';

export interface MCPCardProps {
	endpoint: string;
}

export function MCPCard({ endpoint }: MCPCardProps) {
	return (
		<Card>
			<CardHeader>
				<CardTitle>Model Context Protocol</CardTitle>
				<CardDescription>
					Point an MCP client at this endpoint with a Bearer API key.
				</CardDescription>
			</CardHeader>
			<CardContent className="space-y-3">
				<code className="block break-all rounded-md bg-muted px-3 py-2 font-mono text-xs">
					{endpoint}
				</code>
				<p className="text-sm text-muted-foreground">
					A viewer key reads five tools; a member key adds redacted delivery
					outcomes.
				</p>
				<a
					href={MCP_DOCS_URL}
					className="text-sm font-medium text-primary underline-offset-4 hover:underline"
				>
					Read the MCP docs
				</a>
			</CardContent>
		</Card>
	);
}
