import { useId, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { isApiProblem } from '@/lib/api/client';
import { listAdminWorkspaces } from './api';
import { useDebouncedValue } from './useDebouncedValue';

export interface WorkspaceComboboxProps {
	value: string | null;
	onChange: (workspaceId: string | null) => void;
	label?: string;
	placeholder?: string;
	allowClear?: boolean;
}

export function WorkspaceCombobox({
	value,
	onChange,
	label = 'Workspace',
	placeholder = 'All workspaces',
	allowClear = true,
}: WorkspaceComboboxProps) {
	const inputId = useId();
	const listId = useId();
	const [text, setText] = useState('');
	const [open, setOpen] = useState(false);
	const debounced = useDebouncedValue(text.trim(), 300);
	const query = useQuery({
		queryKey: ['admin', 'workspaces', 'combobox', debounced],
		queryFn: ({ signal }) =>
			listAdminWorkspaces({ query: debounced }, undefined, signal),
		enabled: open && debounced.length > 0,
		staleTime: 60_000,
	});

	const workspaces = query.data?.workspaces ?? [];
	const showResults = open && debounced.length > 0;

	function handleText(next: string) {
		setText(next);
		setOpen(true);
		if (next.trim().length === 0) onChange(null);
	}

	function select(workspaceId: string, name: string) {
		setText(name);
		setOpen(false);
		onChange(workspaceId);
	}

	function clear() {
		setText('');
		setOpen(false);
		onChange(null);
	}

	return (
		<div className="relative space-y-1">
			<div className="flex items-center justify-between gap-2">
				<Label htmlFor={inputId}>{label}</Label>
				{allowClear && value ? (
					<Button
						type="button"
						variant="ghost"
						size="xs"
						aria-label={`Clear ${label.toLowerCase()}`}
						onClick={clear}
					>
						Clear
					</Button>
				) : null}
			</div>
			<Input
				id={inputId}
				type="search"
				role="combobox"
				aria-expanded={showResults}
				aria-controls={listId}
				autoComplete="off"
				placeholder={placeholder}
				value={text}
				onChange={(event) => handleText(event.target.value)}
				onFocus={() => setOpen(true)}
				onKeyDown={(event) => {
					if (event.key === 'Escape') setOpen(false);
				}}
			/>
			{showResults ? (
				<div className="absolute z-20 top-full right-0 left-0 mt-1 rounded-lg border bg-popover p-1 shadow-md">
					{query.isPending ? (
						<p className="px-2 py-1 text-sm text-muted-foreground">
							Searching…
						</p>
					) : query.isError ? (
						<p className="px-2 py-1 text-sm text-destructive">
							{isApiProblem(query.error)
								? (query.error.problem.detail ?? query.error.problem.title)
								: 'Could not search workspaces.'}
						</p>
					) : workspaces.length === 0 ? (
						<p className="px-2 py-1 text-sm text-muted-foreground">
							No workspaces found.
						</p>
					) : (
						<ul id={listId} role="listbox" className="space-y-0.5">
							{workspaces.map((workspace) => (
								<li key={workspace.id}>
									<button
										type="button"
										role="option"
										aria-selected={workspace.id === value}
										className="flex w-full flex-col rounded-md px-2 py-1 text-left text-sm hover:bg-muted"
										onClick={() => select(workspace.id, workspace.name)}
									>
										<span>{workspace.name}</span>
										<span className="text-xs text-muted-foreground">
											{workspace.slug}
										</span>
									</button>
								</li>
							))}
						</ul>
					)}
				</div>
			) : null}
		</div>
	);
}
