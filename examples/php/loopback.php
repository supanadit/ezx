<?php
// Fix loopback HTTP requests inside a container.
add_filter( 'pre_http_request', function( $preempt, $parsed_args, $url ) {
    if ( strpos( $url, 'localhost' ) !== false ) {
        return wp_remote_request( 'http://127.0.0.1', $parsed_args );
    }
    return $preempt;
}, 10, 3 );
