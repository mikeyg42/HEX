# HEX
This is a work in progress implementation of Hex, the 2-player strategy board game famous for its illlustrious fans, its elegant gameplay, and gameplay simplicity. Because of this simple gameplay, it is not difficult to describe strategies rigorously and subject them to mathematical proof -- the game, and its numerous variants, appear regularly in math journals, in topics extending past graph theory and into the fields of combinatorics, algebraic topology, and even mathematical econonmics. 

This repo is so far all back end tech. Because this is for learning mostly, I am making the project have the ability to horizontally scale with ease. I'm using Golang sa much as possible, taking advantage of its powerful concurrency tools. loosely following a cqrs design. For memory I'm relying on Redis caching via go-redis package, w/ GORM+postgresql persistence as a failsafe. I've incorporated a powerful zap logger w/ lumberjack for rolling records. It'll be deployed one day using Docker and google cloud, once I work out the front end.
