<script lang="ts">
	let { timestamp }: { timestamp: string } = $props();

	let now = $state(Date.now());

	$effect(() => {
		const id = setInterval(() => (now = Date.now()), 10000);
		return () => clearInterval(id);
	});

	const text = $derived.by(() => {
		const diff = Math.max(0, now - new Date(timestamp).getTime());
		const seconds = Math.floor(diff / 1000);
		if (seconds < 60) return `${seconds}s ago`;
		const minutes = Math.floor(seconds / 60);
		if (minutes < 60) return `${minutes}m ago`;
		const hours = Math.floor(minutes / 60);
		if (hours < 24) return `${hours}h ago`;
		return `${Math.floor(hours / 24)}d ago`;
	});
</script>

<time datetime={timestamp} title={new Date(timestamp).toLocaleString()} class="text-foreground-muted whitespace-nowrap">
	{text}
</time>
