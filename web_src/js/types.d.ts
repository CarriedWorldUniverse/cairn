interface Window {
  config?: {
    appUrl: string;
  }
}

declare module '*.vue' {
  import Vue from 'vue';
  export default Vue;
}
