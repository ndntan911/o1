import { Link } from '@tanstack/react-router';
import { CheckIcon, ChevronsUpDownIcon } from 'lucide-react';
import { Button } from '@/components/ui/button';
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuLabel,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import type { Workspace } from '@/lib/api/types';

export interface WorkspaceSwitcherProps {
	workspaces: Workspace[];
	current?: Workspace;
	onNavigate?: () => void;
}

export function WorkspaceSwitcher({
	workspaces,
	current,
	onNavigate,
}: WorkspaceSwitcherProps) {
	return (
		<DropdownMenu>
			<DropdownMenuTrigger asChild>
				<Button
					type="button"
					variant="outline"
					className="w-full justify-between"
					aria-label="Workspace switcher"
				>
					<span className="truncate">
						{current?.name ?? 'Select workspace'}
					</span>
					<ChevronsUpDownIcon className="size-4 shrink-0 text-muted-foreground" />
				</Button>
			</DropdownMenuTrigger>
			<DropdownMenuContent align="start" className="w-64">
				<DropdownMenuLabel>Workspaces</DropdownMenuLabel>
				{workspaces.map((workspace) => (
					<DropdownMenuItem key={workspace.id} asChild>
						<Link
							to="/w/$workspaceSlug/monitors"
							params={{ workspaceSlug: workspace.slug }}
							onClick={onNavigate}
						>
							<span className="truncate">{workspace.name}</span>
							{workspace.id === current?.id ? (
								<CheckIcon className="ml-auto size-4" aria-hidden />
							) : null}
						</Link>
					</DropdownMenuItem>
				))}
				<DropdownMenuSeparator />
				<DropdownMenuItem asChild>
					<Link to="/workspaces" onClick={onNavigate}>
						All workspaces
					</Link>
				</DropdownMenuItem>
			</DropdownMenuContent>
		</DropdownMenu>
	);
}
