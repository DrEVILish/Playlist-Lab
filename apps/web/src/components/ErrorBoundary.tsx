import { Component, ReactNode } from 'react';

interface Props {
  children: ReactNode;
  fallback?: ReactNode;
}

interface State {
  hasError: boolean;
  error: Error | null;
}

// Once a lazy(() => import(...)) factory's promise rejects, React keeps
// reusing that same settled (rejected) promise for the lifetime of the page -
// re-rendering the component (e.g. via this boundary's own "Try again") hits
// the exact same rejection immediately, it does not retry the network
// request. The only real fix for a stale chunk reference is a full page
// reload, which fetches the current index.html/chunk manifest.
const CHUNK_ERROR_PATTERN = /dynamically imported module|importing a module script failed|chunkloaderror/i;

function isChunkLoadError(error: Error | null): boolean {
  if (!error) return false;
  return error.name === 'ChunkLoadError' || CHUNK_ERROR_PATTERN.test(error.message);
}

// Guards against a reload loop if the reload itself doesn't fix it (e.g.
// genuinely offline) - cleared once the app renders successfully, see
// App.tsx, so a *future* stale-chunk error still gets one fresh retry.
export const CHUNK_RELOAD_GUARD_KEY = 'plr-chunk-reload-attempted';

export class ErrorBoundary extends Component<Props, State> {
  constructor(props: Props) {
    super(props);
    this.state = { hasError: false, error: null };
  }

  static getDerivedStateFromError(error: Error): State {
    return { hasError: true, error };
  }

  componentDidCatch(error: Error, errorInfo: any) {
    console.error('ErrorBoundary caught an error:', error, errorInfo);

    if (isChunkLoadError(error) && !sessionStorage.getItem(CHUNK_RELOAD_GUARD_KEY)) {
      sessionStorage.setItem(CHUNK_RELOAD_GUARD_KEY, '1');
      window.location.reload();
    }
  }

  render() {
    if (this.state.hasError) {
      if (this.props.fallback) {
        return this.props.fallback;
      }

      if (isChunkLoadError(this.state.error)) {
        return (
          <div style={{ padding: '3rem', textAlign: 'center', color: 'var(--text-secondary)' }}>
            A new version of Playlist Lab is available - reloading...
          </div>
        );
      }

      return (
        <div style={{
          padding: '2rem',
          backgroundColor: 'rgba(244, 67, 54, 0.1)',
          border: '1px solid var(--error)',
          borderRadius: '4px',
          color: 'var(--error)',
        }}>
          <h2>Something went wrong</h2>
          <p>{this.state.error?.message || 'An unexpected error occurred'}</p>
          <button
            className="btn btn-primary"
            onClick={() => this.setState({ hasError: false, error: null })}
            style={{ marginTop: '1rem' }}
          >
            Try again
          </button>
        </div>
      );
    }

    return this.props.children;
  }
}
