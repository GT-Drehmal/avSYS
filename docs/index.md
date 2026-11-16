---
layout: home
title: avSYS
titleTemplate: A Minecraft server panel for Kubernetes
editLink: false

hero:
  name: avSYS
  text: A Minecraft server panel for Kubernetes
  tagline: Make servers for your friends or your club in a minute, let players in, and keep every world safe. In the browser, or over SSH.
  image:
    light: /mark-light.svg
    dark: /mark-dark.svg
    alt: avSYS
  actions:
    - theme: brand
      text: User guide
      link: /user-guide
    - theme: alt
      text: Install
      link: /install
    - theme: alt
      text: GitHub
      link: https://github.com/GT-Drehmal/avSYS
---

<script setup>
import modpack from "./images/modpack.png";
import players from "./images/players.png";
import servers from "./images/servers.png";
import backups from "./images/backups.png";
import files from "./images/files.png";
import signIn from "./images/sign-in.png";

const features = [
  {
    title: "Servers from anything",
    details: "Any version and loader, a modpack, a template, a copy, or a world you upload. Add mods and plugins from Modrinth or CurseForge.",
    image: modpack,
    link: "/user-guide#making-a-server",
  },
  {
    title: "Players, Java and Bedrock",
    details: "Members are whitelisted by role. Saved player lists go on any number of servers.",
    image: players,
    link: "/user-guide#players",
  },
  {
    title: "Sleeps when empty",
    details: "One address for every server. A server nobody is on goes to sleep and wakes when someone joins.",
    image: servers,
    link: "/user-guide#sleeping-and-waking",
  },
  {
    title: "Snapshots and offsite backups",
    details: "Every server is saved daily, and any time with one click, while it runs. Put one back, or copy them offsite.",
    image: backups,
    link: "/user-guide#backups-and-snapshots",
  },
  {
    title: "Files, terminal and SFTP",
    details: "A file editor and a terminal in the browser. Or ssh, scp, sftp and VS Code with your own keys.",
    image: files,
    link: "/user-guide#ssh-sftp-and-vs-code",
  },
  {
    title: "Sign in your way",
    details: "A password with two-factor, GitHub, Discord, Google, or your own sign-in. People join by invite link.",
    image: signIn,
    link: "/user-guide#signing-in",
  },
];
</script>

<Features :items="features" />
