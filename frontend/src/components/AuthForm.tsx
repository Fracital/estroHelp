import GatePage from './GatePage';

export default function AuthForm({ mode }: { mode: 'login' | 'register' }) {
  return <GatePage mode={mode} />;
}
