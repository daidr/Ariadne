import 'dou-ui/style.css'
import './pwa.css'
import { createApp } from 'vue'
import { createWebHistory, RouterView } from 'vue-router'
import App from './App.vue'
import { initToken } from './api'
import { watchPresence } from './presence'
import { createWorkspaceRouter } from './workspace-route'

initToken()
createApp(RouterView).use(createWorkspaceRouter(createWebHistory(), App)).mount('#app')
watchPresence()
