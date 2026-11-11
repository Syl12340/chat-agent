export const RemoteProtocolVersion = 1;

export function createTransportState(carrier = 'http') {
	return {protocol: 0, generation: '', capabilities: [], carrier, phase: navigator.onLine ? 'connecting' : 'offline', resync: false};
}

export function transportHeaders(transport) {
	const headers = {'X-Chat-Carrier': transport?.carrier === 'securewire' ? 'securewire' : 'http'};
	if (transport?.generation) headers['X-Chat-Generation'] = transport.generation;
	return headers;
}

export function observeTransport(transport, response) {
	const generation = response.headers.get('X-Chat-Generation') || '';
	const protocol = Number(response.headers.get('X-Chat-Protocol') || 0);
	if (!generation || !Number.isInteger(protocol) || protocol < 1) return {changed:false};
	const previous = transport.generation;
	transport.protocol = protocol;
	transport.generation = generation;
	transport.phase = 'connected';
	transport.resync = Boolean(previous && previous !== generation);
	return {changed:transport.resync, previous, generation};
}

export function adoptTransportInfo(transport, info) {
	if (!info || info.protocol_version !== RemoteProtocolVersion || typeof info.generation !== 'string') return false;
	const previous = transport.generation;
	transport.protocol = info.protocol_version;
	transport.generation = info.generation;
	transport.capabilities = Array.isArray(info.capabilities) ? [...info.capabilities] : [];
	transport.peerID = typeof info.peer_id === 'string' ? info.peer_id : '';
	transport.phase = 'connected';
	transport.resync = Boolean(previous && previous !== info.generation);
	return transport.resync;
}

export function transportFailed(transport) {
	transport.phase = navigator.onLine ? 'reconnecting' : 'offline';
}

export function transportNetworkChanged(transport) {
	if (!navigator.onLine) transport.phase = 'offline';
	else if (transport.phase === 'offline') transport.phase = 'reconnecting';
}
